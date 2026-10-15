package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// Second factors (spec §7.6): TOTP, passkeys and one-time recovery codes. All short-lived state
// (a pending sign-in, a TOTP setup, a WebAuthn ceremony) is an auth_challenges row that is taken
// once; a failed attempt puts the row back unchanged so the user can try again within its
// lifetime, under the same rate limits as passwords.

const (
	loginCookie       = "tr_login"
	loginCookiePath   = "/api/auth/login"
	loginTTL          = 5 * time.Minute
	ceremonyTTL       = 5 * time.Minute
	totpSetupTTL      = 10 * time.Minute
	totpPeriod        = 30
	totpIssuer        = "Telesphorepo"
	recoveryCodeCount = 10
	recoveryCodeLen   = 16 // base32 characters (80 bits)

	// auth_challenges.kind values owned by this file.
	chLogin           = "login"
	chTOTPSetup       = "totp_setup"
	chPasskeyRegister = "webauthn_register"
	chPasskeyAssert   = "webauthn_assert"
)

var (
	totpOpts = totp.ValidateOpts{Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}
	b32      = base32.StdEncoding.WithPadding(base32.NoPadding)

	// ErrLoginExpired: the pending sign-in is unknown or expired; the user enters the password
	// again. It wraps ErrBadCredentials.
	ErrLoginExpired = fmt.Errorf("%w: sign-in expired; enter your password again", ErrBadCredentials)

	errNoSealer = errors.New("auth: no sealer set for TOTP secrets")
)

// TOTPFactor is a confirmed TOTP authenticator.
type TOTPFactor struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Passkey is a registered WebAuthn credential.
type Passkey struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// Factors is a user's second-factor overview. SecondFactor is true when the user has a TOTP
// factor or a passkey (recovery codes alone do not count).
type Factors struct {
	TOTP          []TOTPFactor `json:"totp"`
	Passkeys      []Passkey    `json:"passkeys"`
	RecoveryCodes int64        `json:"recovery_codes"` // unused codes left
	SecondFactor  bool         `json:"second_factor"`
}

// TOTPSetup is a TOTP enrolment that waits for a code. Secret and URL are shown once.
type TOTPSetup struct {
	SetupID string `json:"setup_id"`
	Secret  string `json:"secret"`      // base32, for manual entry
	URL     string `json:"otpauth_url"` // otpauth://totp/..., for a QR code
}

// SecondFactorInput carries exactly one proof: a TOTP code, a passkey assertion (the browser's
// PublicKeyCredential JSON) or a recovery code.
type SecondFactorInput struct {
	TOTPCode     string          `json:"totp_code"`
	Passkey      json.RawMessage `json:"passkey"`
	RecoveryCode string          `json:"recovery_code"`
}

// --- challenges ---

// newSecret returns a random 32-byte value (base64url) and the SHA-256 its row is stored under.
func newSecret() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	v := base64.RawURLEncoding.EncodeToString(raw)
	h, _ := hashSession(v)
	return v, h, nil
}

// ceremonyID is the auth_challenges id of a WebAuthn ceremony: SHA-256 of its challenge.
func ceremonyID(challenge string) []byte {
	sum := sha256.Sum256([]byte(challenge))
	return sum[:]
}

func (s *Service) putChallenge(ctx context.Context, kind string, id []byte, userID int64, data []byte, ttl time.Duration) error {
	now := time.Now().Unix()
	if data == nil {
		data = []byte{}
	}
	return s.wq.CreateChallenge(ctx, dbq.CreateChallengeParams{
		IDHash: id, Kind: kind, UserID: sql.NullInt64{Int64: userID, Valid: true},
		Data: data, CreatedAt: now, ExpiresAt: now + int64(ttl/time.Second)})
}

func (s *Service) takeChallenge(ctx context.Context, kind string, id []byte) (dbq.TakeChallengeRow, error) {
	return s.wq.TakeChallenge(ctx, dbq.TakeChallengeParams{IDHash: id, Kind: kind, Now: time.Now().Unix()})
}

// restoreChallenge puts a taken challenge back unchanged, with its original expiry.
func (s *Service) restoreChallenge(ctx context.Context, kind string, id []byte, row dbq.TakeChallengeRow) error {
	data := row.Data
	if data == nil {
		data = []byte{}
	}
	return s.wq.CreateChallenge(context.WithoutCancel(ctx), dbq.CreateChallengeParams{
		IDHash: id, Kind: kind, UserID: row.UserID, DeviceID: row.DeviceID,
		Data: data, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt})
}

// --- sign-in ---

// factorKinds lists the factor kinds a user can sign in with, in the order totp, passkey,
// recovery. has is true when the user has a TOTP factor or a passkey; recovery codes are
// offered only then.
func (s *Service) factorKinds(ctx context.Context, userID int64) ([]string, bool, error) {
	ts, err := s.rq.ListUserTOTP(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	pks, err := s.rq.ListUserPasskeys(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	kinds := []string{}
	if len(ts) > 0 {
		kinds = append(kinds, "totp")
	}
	if len(pks) > 0 {
		kinds = append(kinds, "passkey")
	}
	has := len(kinds) > 0
	if has {
		n, err := s.rq.CountUnusedRecoveryCodes(ctx, userID)
		if err != nil {
			return nil, false, err
		}
		if n > 0 {
			kinds = append(kinds, "recovery")
		}
	}
	return kinds, has, nil
}

func (s *Service) startPendingLogin(w http.ResponseWriter, r *http.Request, userID int64) error {
	v, h, err := newSecret()
	if err != nil {
		return err
	}
	if err := s.putChallenge(r.Context(), chLogin, h, userID, nil, loginTTL); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: loginCookie, Value: v, Path: loginCookiePath,
		MaxAge: int(loginTTL / time.Second), Expires: time.Now().Add(loginTTL),
		HttpOnly: true, Secure: s.cfg().CookieSecure, SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func (s *Service) clearLoginCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: loginCookie, Value: "", Path: loginCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: s.cfg().CookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

// takePendingLogin takes the pending sign-in named by the tr_login cookie.
func (s *Service) takePendingLogin(r *http.Request) (dbq.TakeChallengeRow, []byte, error) {
	c, err := r.Cookie(loginCookie)
	if err != nil {
		return dbq.TakeChallengeRow{}, nil, ErrLoginExpired
	}
	h, ok := hashSession(c.Value)
	if !ok {
		return dbq.TakeChallengeRow{}, nil, ErrLoginExpired
	}
	row, err := s.takeChallenge(r.Context(), chLogin, h)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !row.UserID.Valid) {
		return dbq.TakeChallengeRow{}, nil, ErrLoginExpired
	}
	if err != nil {
		return dbq.TakeChallengeRow{}, nil, err
	}
	return row, h, nil
}

// LoginPasskeyOptions starts a passkey assertion for the pending sign-in (cookie tr_login).
// The answer is the options for navigator.credentials.get().
func (s *Service) LoginPasskeyOptions(r *http.Request) (*protocol.CredentialAssertion, error) {
	row, h, err := s.takePendingLogin(r)
	if err != nil {
		return nil, err
	}
	if err := s.restoreChallenge(r.Context(), chLogin, h, row); err != nil {
		return nil, err
	}
	return s.BeginPasskeyAssertion(r.Context(), row.UserID.Int64)
}

// LoginSecondFactor completes the pending sign-in (cookie tr_login) with one proof and starts
// the session. A wrong proof keeps the sign-in pending and counts as a failed attempt for the
// client IP and the username, like a wrong password.
func (s *Service) LoginSecondFactor(w http.ResponseWriter, r *http.Request, in SecondFactorInput) (*User, error) {
	ctx := r.Context()
	row, h, err := s.takePendingLogin(r)
	if err != nil {
		if errors.Is(err, ErrLoginExpired) {
			s.clearLoginCookie(w)
		}
		return nil, err
	}
	uid := row.UserID.Int64
	p, err := s.principalByID(ctx, uid)
	if err != nil {
		if errors.Is(err, ErrBadCredentials) {
			s.clearLoginCookie(w)
			return nil, ErrLoginExpired
		}
		return nil, err
	}
	if p.Perms != 0 && !s.cfg().AdminPasswordLogin {
		s.clearLoginCookie(w)
		return nil, ErrPasswordLoginDisabled
	}
	keys := limitKeys(s.clientIP(r), p.Username)
	if d := s.limits.begin(keys...); d > 0 {
		if err := s.restoreChallenge(ctx, chLogin, h, row); err != nil {
			return nil, err
		}
		setRetryAfter(w, d)
		return nil, ErrRateLimited
	}
	ok, err := s.checkFactor(ctx, uid, in, true)
	s.limits.end(err == nil && !ok, keys...)
	if err != nil || !ok {
		if rerr := s.restoreChallenge(ctx, chLogin, h, row); rerr != nil {
			return nil, rerr
		}
		if err != nil {
			return nil, err
		}
		s.recordSignInFailed(r, p.Username, "second factor")
		return nil, ErrBadSecondFactor
	}
	s.clearLoginCookie(w)
	if err := s.StartSession(w, r, uid, s.cfg().SessionTTL); err != nil {
		return nil, err
	}
	method := "recovery"
	switch {
	case in.TOTPCode != "":
		method = "totp"
	case len(in.Passkey) > 0 && string(in.Passkey) != "null":
		method = "passkey"
	}
	s.RecordSignIn(r, uid, p.Username, method)
	return s.GetUser(ctx, p.Username)
}

// VerifySecondFactor checks a fresh TOTP code or a passkey assertion (JSON from the browser) for
// userID. The assertion answers options from BeginPasskeyAssertion. In dev mode a user without a
// factor passes without a proof. Errors: ErrNoSecondFactor (the user has none), ErrInvalid (no proof or both), ErrBadSecondFactor, a rate-limit error
// (errors.Is ErrRateLimited; RetryAfter gives the wait) or an internal error. Failures count
// against the username like failed passwords.
func (s *Service) VerifySecondFactor(ctx context.Context, userID int64, totpCode string, passkeyAssertion json.RawMessage) error {
	p, err := s.principalByID(ctx, userID)
	if err != nil {
		return err
	}
	has, err := s.rq.HasSecondFactor(ctx, userID)
	if err != nil {
		return err
	}
	if has == 0 {
		if s.dev {
			return nil // dev mode: the factor is optional; one that exists is still checked
		}
		return ErrNoSecondFactor
	}
	keys := []string{userLimitKey(p.Username)}
	if d := s.limits.begin(keys...); d > 0 {
		return &rateLimitedError{d}
	}
	ok, err := s.checkFactor(ctx, userID, SecondFactorInput{TOTPCode: totpCode, Passkey: passkeyAssertion}, false)
	s.limits.end(err == nil && !ok, keys...)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBadSecondFactor
	}
	return nil
}

// checkFactor verifies exactly one proof. ok=false with a nil error means it did not verify.
func (s *Service) checkFactor(ctx context.Context, userID int64, in SecondFactorInput, allowRecovery bool) (bool, error) {
	hasPasskey := len(in.Passkey) > 0 && string(in.Passkey) != "null"
	n := 0
	for _, set := range []bool{in.TOTPCode != "", hasPasskey, in.RecoveryCode != ""} {
		if set {
			n++
		}
	}
	if n != 1 || (in.RecoveryCode != "" && !allowRecovery) {
		if allowRecovery {
			return false, fmt.Errorf("%w: give one of totp_code, passkey or recovery_code", ErrInvalid)
		}
		return false, fmt.Errorf("%w: give one of totp_code or passkey", ErrInvalid)
	}
	switch {
	case in.TOTPCode != "":
		return s.checkTOTP(ctx, userID, in.TOTPCode)
	case hasPasskey:
		return s.checkPasskey(ctx, userID, in.Passkey)
	default:
		return s.useRecoveryCode(ctx, userID, in.RecoveryCode)
	}
}

// --- TOTP ---

func totpAAD(id int64) string { return "user_totp:" + strconv.FormatInt(id, 10) }

func totpSetupAAD(userID int64) string {
	return "auth_challenges:" + chTOTPSetup + ":" + strconv.FormatInt(userID, 10)
}

// normCode strips spaces from a TOTP code; ok is false unless six digits remain.
func normCode(code string) (string, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return "", false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return code, true
}

// matchTOTP returns the time step (current ±1) whose code equals code and that is later than
// after.
func matchTOTP(secret, code string, now time.Time, after int64) (int64, bool) {
	cur := now.Unix() / totpPeriod
	for step := cur - 1; step <= cur+1; step++ {
		if step <= after {
			continue
		}
		want, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpPeriod, 0), totpOpts)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// checkTOTP accepts a code of any of the user's TOTP factors once per time step.
func (s *Service) checkTOTP(ctx context.Context, userID int64, code string) (bool, error) {
	code, ok := normCode(code)
	if !ok {
		return false, nil
	}
	if s.sealer == nil {
		return false, errNoSealer
	}
	rows, err := s.rq.ListUserTOTP(ctx, userID)
	if err != nil {
		return false, err
	}
	now := time.Now()
	for _, row := range rows {
		secret, err := s.sealer.Decrypt(totpAAD(row.ID), row.Secret)
		if err != nil {
			return false, err
		}
		step, ok := matchTOTP(string(secret), code, now, row.LastStep)
		if !ok {
			continue
		}
		n, err := s.wq.UseTOTPStep(ctx, dbq.UseTOTPStepParams{Step: step, ID: row.ID})
		if err != nil {
			return false, err
		}
		if n == 1 {
			return true, nil
		}
	}
	return false, nil
}

type totpPending struct {
	Secret string `json:"secret"`
	Name   string `json:"name"`
}

// BeginTOTP starts a TOTP enrolment. The factor counts only after ConfirmTOTP.
func (s *Service) BeginTOTP(ctx context.Context, userID int64, name string) (*TOTPSetup, error) {
	if !validTokenName(name) {
		return nil, fmt.Errorf("%w: name must be 1-100 printable characters", ErrInvalid)
	}
	if s.sealer == nil {
		return nil, errNoSealer
	}
	p, err := s.principalByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer: totpIssuer, AccountName: p.Username, Period: totpPeriod,
		Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(totpPending{Secret: key.Secret(), Name: name})
	if err != nil {
		return nil, err
	}
	v, h, err := newSecret()
	if err != nil {
		return nil, err
	}
	if err := s.putChallenge(ctx, chTOTPSetup, h, userID, s.sealer.Encrypt(totpSetupAAD(userID), data), totpSetupTTL); err != nil {
		return nil, err
	}
	return &TOTPSetup{SetupID: v, Secret: key.Secret(), URL: key.URL()}, nil
}

// ConfirmTOTP finishes an enrolment with a current code from the authenticator. A wrong code
// keeps the setup pending and counts as a failed attempt for the username.
func (s *Service) ConfirmTOTP(ctx context.Context, userID int64, setupID, code string) (*TOTPFactor, error) {
	expired := fmt.Errorf("%w: TOTP setup expired; start again", ErrInvalid)
	h, ok := hashSession(setupID)
	if !ok {
		return nil, expired
	}
	if s.sealer == nil {
		return nil, errNoSealer
	}
	p, err := s.principalByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	keys := []string{userLimitKey(p.Username)}
	if d := s.limits.begin(keys...); d > 0 {
		return nil, &rateLimitedError{d}
	}
	failed := false
	defer func() { s.limits.end(failed, keys...) }()

	row, err := s.takeChallenge(ctx, chTOTPSetup, h)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.UserID.Int64 != userID) {
		if err == nil {
			if rerr := s.restoreChallenge(ctx, chTOTPSetup, h, row); rerr != nil {
				return nil, rerr
			}
		}
		return nil, expired
	}
	if err != nil {
		return nil, err
	}
	plain, err := s.sealer.Decrypt(totpSetupAAD(userID), row.Data)
	if err != nil {
		return nil, err
	}
	var pend totpPending
	if err := json.Unmarshal(plain, &pend); err != nil {
		return nil, err
	}
	now := time.Now()
	c, ok := normCode(code)
	var step int64
	if ok {
		step, ok = matchTOTP(pend.Secret, c, now, 0)
	}
	if !ok {
		failed = true
		if err := s.restoreChallenge(ctx, chTOTPSetup, h, row); err != nil {
			return nil, err
		}
		return nil, ErrBadSecondFactor
	}
	f := &TOTPFactor{Name: pend.Name, CreatedAt: time.Unix(now.Unix(), 0)}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := s.wq.WithTx(tx)
		id, err := q.CreateTOTP(ctx, dbq.CreateTOTPParams{
			UserID: userID, Name: pend.Name, CreatedAt: now.Unix(), LastStep: step})
		if err != nil {
			return err
		}
		f.ID = id
		return q.SetTOTPSecret(ctx, dbq.SetTOTPSecretParams{
			Secret: s.sealer.Encrypt(totpAAD(id), []byte(pend.Secret)), ID: id})
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// DeleteTOTP removes one of the user's TOTP factors.
func (s *Service) DeleteTOTP(ctx context.Context, userID, id int64) error {
	n, err := s.wq.DeleteUserTOTP(ctx, dbq.DeleteUserTOTPParams{ID: id, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- passkeys ---

// webUser is a user as go-webauthn sees it. The user handle is the user id (8 bytes, big
// endian).
type webUser struct {
	id    int64
	name  string
	creds []webauthn.Credential
}

func (u *webUser) WebAuthnID() []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(u.id))
	return b
}
func (u *webUser) WebAuthnName() string                       { return u.name }
func (u *webUser) WebAuthnDisplayName() string                { return u.name }
func (u *webUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (s *Service) webAuthnUser(ctx context.Context, userID int64) (*webUser, error) {
	p, err := s.principalByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.rq.ListUserPasskeys(ctx, userID)
	if err != nil {
		return nil, err
	}
	u := &webUser{id: userID, name: p.Username, creds: make([]webauthn.Credential, 0, len(rows))}
	for _, row := range rows {
		var c webauthn.Credential
		if err := json.Unmarshal(row.Credential, &c); err != nil {
			return nil, err
		}
		u.creds = append(u.creds, c)
	}
	return u, nil
}

// webAuthn builds the relying party from the current base_url: the RP ID is its host name and
// the accepted origin is its scheme and host. For localhost, the dev ports (Vite :5173 and the
// server :8080) are accepted too; no other machine can serve a page from this machine's localhost.
func (s *Service) webAuthn() (*webauthn.WebAuthn, error) {
	base := strings.TrimSpace(s.cfg().BaseURL)
	u, err := url.Parse(base)
	if base == "" || err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, ErrPasskeysUnavailable
	}
	origins := []string{u.Scheme + "://" + u.Host}
	if u.Hostname() == "localhost" {
		origins = append(origins, "http://localhost:5173", "http://localhost:8080")
	}
	t := webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTTL, TimeoutUVD: ceremonyTTL}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: totpIssuer,
		RPOrigins:     origins,
		Timeouts:      webauthn.TimeoutsConfig{Login: t, Registration: t},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPasskeysUnavailable, err)
	}
	return wa, nil
}

// BeginPasskeyRegistration starts registering a passkey. The answer is the options for
// navigator.credentials.create().
func (s *Service) BeginPasskeyRegistration(ctx context.Context, userID int64) (*protocol.CredentialCreation, error) {
	wa, err := s.webAuthn()
	if err != nil {
		return nil, err
	}
	u, err := s.webAuthnUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	opts, sd, err := wa.BeginRegistration(u,
		webauthn.WithExclusions(webauthn.Credentials(u.creds).CredentialDescriptors()),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred))
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(sd)
	if err != nil {
		return nil, err
	}
	if err := s.putChallenge(ctx, chPasskeyRegister, ceremonyID(sd.Challenge), userID, data, ceremonyTTL); err != nil {
		return nil, err
	}
	return opts, nil
}

// FinishPasskeyRegistration verifies the browser's PublicKeyCredential JSON from
// navigator.credentials.create() and stores the passkey under name.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, userID int64, name string, credential json.RawMessage) (*Passkey, error) {
	if !validTokenName(name) {
		return nil, fmt.Errorf("%w: name must be 1-100 printable characters", ErrInvalid)
	}
	bad := fmt.Errorf("%w: the passkey did not verify; start again", ErrInvalid)
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credential)
	if err != nil {
		return nil, bad
	}
	row, err := s.takeChallenge(ctx, chPasskeyRegister, ceremonyID(parsed.Response.CollectedClientData.Challenge))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, bad
	}
	if err != nil {
		return nil, err
	}
	if row.UserID.Int64 != userID {
		return nil, bad
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(row.Data, &sd); err != nil {
		return nil, err
	}
	wa, err := s.webAuthn()
	if err != nil {
		return nil, err
	}
	u, err := s.webAuthnUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	cred, err := wa.CreateCredential(u, sd, parsed)
	if err != nil {
		return nil, bad
	}
	js, err := json.Marshal(cred)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	id, err := s.wq.CreatePasskey(ctx, dbq.CreatePasskeyParams{
		UserID: userID, CredentialID: cred.ID, PublicKey: cred.PublicKey,
		SignCount: int64(cred.Authenticator.SignCount), Credential: js, Name: name, CreatedAt: now})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrExists
		}
		return nil, err
	}
	return &Passkey{ID: id, Name: name, CreatedAt: time.Unix(now, 0)}, nil
}

// BeginPasskeyAssertion starts a passkey check for userID (sign-in, or a fresh check before a
// device approval or a manual publish session). The answer is the options for
// navigator.credentials.get(); ErrNotFound when the user has no passkey.
func (s *Service) BeginPasskeyAssertion(ctx context.Context, userID int64) (*protocol.CredentialAssertion, error) {
	wa, err := s.webAuthn()
	if err != nil {
		return nil, err
	}
	u, err := s.webAuthnUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(u.creds) == 0 {
		return nil, ErrNotFound
	}
	opts, sd, err := wa.BeginLogin(u)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(sd)
	if err != nil {
		return nil, err
	}
	if err := s.putChallenge(ctx, chPasskeyAssert, ceremonyID(sd.Challenge), userID, data, ceremonyTTL); err != nil {
		return nil, err
	}
	return opts, nil
}

// checkPasskey verifies an assertion against the ceremony its challenge names. The ceremony
// is single use; a cloned-authenticator warning fails the check.
func (s *Service) checkPasskey(ctx context.Context, userID int64, assertion json.RawMessage) (bool, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(assertion)
	if err != nil {
		return false, nil
	}
	row, err := s.takeChallenge(ctx, chPasskeyAssert, ceremonyID(parsed.Response.CollectedClientData.Challenge))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.UserID.Int64 != userID {
		return false, nil
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(row.Data, &sd); err != nil {
		return false, err
	}
	wa, err := s.webAuthn()
	if err != nil {
		return false, err
	}
	u, err := s.webAuthnUser(ctx, userID)
	if err != nil {
		return false, err
	}
	cred, err := wa.ValidateLogin(u, sd, parsed)
	if err != nil || cred.Authenticator.CloneWarning {
		return false, nil
	}
	pk, err := s.wq.GetPasskeyByCredentialID(ctx, cred.ID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && pk.UserID != userID) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	js, err := json.Marshal(cred)
	if err != nil {
		return false, err
	}
	if err := s.wq.UpdatePasskeyUse(ctx, dbq.UpdatePasskeyUseParams{
		SignCount: int64(cred.Authenticator.SignCount), Credential: js,
		LastUsedAt: sql.NullInt64{Int64: time.Now().Unix(), Valid: true}, ID: pk.ID}); err != nil {
		return false, err
	}
	return true, nil
}

// DeletePasskey removes one of the user's passkeys.
func (s *Service) DeletePasskey(ctx context.Context, userID, id int64) error {
	n, err := s.wq.DeleteUserPasskey(ctx, dbq.DeleteUserPasskeyParams{ID: id, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- recovery codes ---

// normRecoveryCode lower-cases a code and strips dashes and spaces.
func normRecoveryCode(code string) (string, bool) {
	code = strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	return code, len(code) == recoveryCodeLen
}

// GenerateRecoveryCodes replaces the user's recovery codes with 10 new ones, returned once in
// the form "abcd-efgh-ijkl-mnop". Only argon2id hashes are stored.
func (s *Service) GenerateRecoveryCodes(ctx context.Context, userID int64) ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	hashes := make([]string, recoveryCodeCount)
	for i := range codes {
		raw := make([]byte, recoveryCodeLen*5/8)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		c := strings.ToLower(b32.EncodeToString(raw))
		h, err := s.hashPassword(ctx, c)
		if err != nil {
			return nil, err
		}
		codes[i] = c[0:4] + "-" + c[4:8] + "-" + c[8:12] + "-" + c[12:16]
		hashes[i] = h
	}
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := s.wq.WithTx(tx)
		if err := q.DeleteUserRecoveryCodes(ctx, userID); err != nil {
			return err
		}
		for _, h := range hashes {
			if err := q.InsertRecoveryCode(ctx, dbq.InsertRecoveryCodeParams{UserID: userID, CodeHash: h}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return codes, nil
}

// useRecoveryCode marks a matching unused code as used.
func (s *Service) useRecoveryCode(ctx context.Context, userID int64, code string) (bool, error) {
	code, ok := normRecoveryCode(code)
	if !ok {
		return false, nil
	}
	hashes, err := s.rq.ListUnusedRecoveryCodes(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, h := range hashes {
		match, err := s.verifyPassword(ctx, h, code)
		if err != nil && !errors.Is(err, errBadHash) {
			return false, err
		}
		if !match {
			continue
		}
		n, err := s.wq.UseRecoveryCode(ctx, dbq.UseRecoveryCodeParams{
			UsedAt: sql.NullInt64{Int64: time.Now().Unix(), Valid: true}, UserID: userID, CodeHash: h})
		if err != nil {
			return false, err
		}
		return n == 1, nil
	}
	return false, nil
}

// --- overview ---

// ListFactors returns the user's TOTP factors, passkeys and the number of unused recovery codes.
func (s *Service) ListFactors(ctx context.Context, userID int64) (*Factors, error) {
	ts, err := s.rq.ListUserTOTP(ctx, userID)
	if err != nil {
		return nil, err
	}
	pks, err := s.rq.ListUserPasskeys(ctx, userID)
	if err != nil {
		return nil, err
	}
	n, err := s.rq.CountUnusedRecoveryCodes(ctx, userID)
	if err != nil {
		return nil, err
	}
	f := &Factors{
		TOTP: make([]TOTPFactor, len(ts)), Passkeys: make([]Passkey, len(pks)),
		RecoveryCodes: n, SecondFactor: len(ts)+len(pks) > 0,
	}
	for i, t := range ts {
		f.TOTP[i] = TOTPFactor{ID: t.ID, Name: t.Name, CreatedAt: time.Unix(t.CreatedAt, 0)}
	}
	for i, p := range pks {
		f.Passkeys[i] = Passkey{ID: p.ID, Name: p.Name, CreatedAt: time.Unix(p.CreatedAt, 0), LastUsedAt: optTime(p.LastUsedAt)}
	}
	return f, nil
}
