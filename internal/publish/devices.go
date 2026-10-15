package publish

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

const (
	enrollTTL = 10 * time.Minute
	// pollInterval is the poll interval given to the client. A poll sooner than
	// pollInterval-1 s after the previous one gets slow_down.
	pollInterval = 5 * time.Second
	// userCodeAlphabet has no vowels (no words) and no look-alike letters (RFC 8628 §6.1).
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	// grantKind is the auth_challenges kind of an SSO approval grant; data is the user code.
	grantKind = "device_grant"
	grantTTL  = 5 * time.Minute
)

// Enrollment is the answer to a device enrollment request (RFC 8628 §3.2).
type Enrollment struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// PendingEnrollment is what the approval page shows for a user code.
type PendingEnrollment struct {
	UserCode  string    `json:"user_code"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Device is an enrolled device key. State is "active", "expired" or "revoked". Username is set
// in the admin list.
type Device struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"user_id"`
	Username   string     `json:"username,omitempty"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	State      string     `json:"state"`
}

func deviceState(revoked sql.NullInt64, expires, now int64) string {
	switch {
	case revoked.Valid:
		return "revoked"
	case expires <= now:
		return "expired"
	}
	return "active"
}

func validName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > 100 || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validKind(kind string) bool { return kind == "gradle" || kind == "maven" }

// newUserCode returns a random code "ABCD-EFGH" from userCodeAlphabet.
func newUserCode() string {
	var b [8]byte
	for i := 0; i < len(b); {
		for _, x := range randomBytes(16) {
			if x < 240 && i < len(b) { // 240 = 12 * 20: no modulo bias
				b[i] = userCodeAlphabet[x%20]
				i++
			}
		}
	}
	return string(b[:4]) + "-" + string(b[4:])
}

// normalizeUserCode accepts a user code typed in any case, with or without the dash or spaces.
func normalizeUserCode(in string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(in) {
		switch {
		case r == '-' || r == ' ':
		case r < utf8.RuneSelf && strings.ContainsRune(userCodeAlphabet, r):
			b.WriteRune(r)
		default:
			return "", false
		}
	}
	c := b.String()
	if len(c) != 8 {
		return "", false
	}
	return c[:4] + "-" + c[4:], true
}

// Enroll starts the device flow for a new device key. ip is the caller's address; each address
// gets a small budget of enrollments.
func (s *Service) Enroll(ctx context.Context, ip netip.Addr, name, kind, publicKey string) (*Enrollment, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return nil, errorf(ErrInvalid, "name must be 1 to 100 characters")
	}
	if !validKind(kind) {
		return nil, errorf(ErrInvalid, `kind must be "gradle" or "maven"`)
	}
	key, err := decodeB64URL(publicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errorf(ErrInvalid, "public_key must be a base64url Ed25519 public key (32 bytes)")
	}
	if !s.enrollLimits.allow(ip) {
		return nil, errorf(ErrLimited, "too many enrollments; try again later")
	}
	deviceCode := randomString(32)
	now := time.Now()
	for range 5 {
		code := newUserCode()
		err := s.wq.CreateEnrollment(ctx, dbq.CreateEnrollmentParams{
			DeviceCodeHash: hashBytes([]byte(deviceCode)), UserCode: code, Name: name, Kind: kind,
			PublicKey: key, CreatedAt: now.Unix(), ExpiresAt: now.Add(enrollTTL).Unix(),
		})
		if isUnique(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &Enrollment{
			DeviceCode:      deviceCode,
			UserCode:        code,
			VerificationURL: strings.TrimSuffix(s.baseURL(), "/") + "/#/device",
			ExpiresIn:       int(enrollTTL / time.Second),
			Interval:        int(pollInterval / time.Second),
		}, nil
	}
	return nil, errors.New("could not allocate a user code")
}

// pending loads an enrollment that can still be approved.
func (s *Service) pending(ctx context.Context, userCode string) (dbq.DeviceEnrollment, error) {
	code, ok := normalizeUserCode(userCode)
	if !ok {
		return dbq.DeviceEnrollment{}, errorf(ErrNotFound, "unknown or expired code")
	}
	e, err := s.rq.GetEnrollmentByUserCode(ctx, code)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return e, errorf(ErrNotFound, "unknown or expired code")
	case err != nil:
		return e, err
	case e.ExpiresAt <= time.Now().Unix():
		return e, errorf(ErrNotFound, "unknown or expired code")
	case e.DeviceID.Valid:
		return e, errorf(ErrConflict, "this code was already used")
	}
	return e, nil
}

// PendingEnrollment returns the device name and kind behind a user code for the approval page.
func (s *Service) PendingEnrollment(ctx context.Context, userCode string) (*PendingEnrollment, error) {
	e, err := s.pending(ctx, userCode)
	if err != nil {
		return nil, err
	}
	return &PendingEnrollment{UserCode: e.UserCode, Name: e.Name, Kind: e.Kind, ExpiresAt: unixTime(e.ExpiresAt)}, nil
}

// ApprovalGrant stores a single-use grant that lets the SSO user userID approve the enrollment
// of userCode once, within grantTTL. The SSO callback calls it after a fresh sign-in at the
// identity provider. The code must still wait for approval.
func (s *Service) ApprovalGrant(ctx context.Context, userID int64, userCode string) (string, error) {
	e, err := s.pending(ctx, userCode)
	if err != nil {
		return "", err
	}
	grant := randomString(32)
	now := time.Now().Unix()
	err = s.wq.CreateChallenge(ctx, dbq.CreateChallengeParams{
		IDHash: hashBytes([]byte(grant)), Kind: grantKind, UserID: nullID(userID), Data: []byte(e.UserCode),
		CreatedAt: now, ExpiresAt: now + int64(grantTTL/time.Second),
	})
	if err != nil {
		return "", err
	}
	return grant, nil
}

// takeGrant uses up grant. It must belong to userID and to the enrollment of userCode.
func (s *Service) takeGrant(ctx context.Context, userID int64, userCode, grant string) error {
	if grant == "" {
		return errorf(ErrDenied, "sign in again with single sign-on to approve this device")
	}
	ch, err := s.wq.TakeChallenge(ctx, dbq.TakeChallengeParams{IDHash: hashBytes([]byte(grant)), Kind: grantKind, Now: time.Now().Unix()})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (ch.UserID.Int64 != userID || string(ch.Data) != userCode)) {
		return errorf(ErrDenied, "the single sign-on approval is used, expired or for another code; sign in again")
	}
	return err
}

// Approve creates the device for p after a fresh check of who p is: a second factor for a local
// account, or the grant of a fresh SSO sign-in (ApprovalGrant) for an SSO account. The
// second-factor error comes back unchanged (from auth.Service.VerifySecondFactor).
func (s *Service) Approve(ctx context.Context, p *auth.Principal, userCode, totpCode string, passkey json.RawMessage, grant string) (*Device, error) {
	e, err := s.pending(ctx, userCode)
	if err != nil {
		return nil, err
	}
	switch p.Kind {
	case auth.KindLocal:
		err = s.auth.VerifySecondFactor(ctx, p.UserID, totpCode, passkey)
	case auth.KindOIDC:
		err = s.takeGrant(ctx, p.UserID, e.UserCode, grant)
	default:
		err = errorf(ErrForbidden, "sign in to approve a device")
	}
	if err != nil {
		return nil, err
	}
	now := time.Now()
	exp := now.Add(s.deviceTTL())
	var id int64
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		var err error
		id, err = q.CreateDevice(ctx, dbq.CreateDeviceParams{
			UserID: p.UserID, Name: e.Name, Kind: e.Kind, PublicKey: e.PublicKey,
			CreatedAt: now.Unix(), ExpiresAt: exp.Unix(),
		})
		if isUnique(err) {
			return errorf(ErrConflict, "this device key is already enrolled")
		}
		if err != nil {
			return err
		}
		n, err := q.ApproveEnrollment(ctx, dbq.ApproveEnrollmentParams{
			ApprovedBy: nullID(p.UserID), DeviceID: nullID(id), DeviceCodeHash: e.DeviceCodeHash, Now: now.Unix(),
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return errorf(ErrConflict, "this code was already used or has expired")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Device{
		ID: id, UserID: p.UserID, Name: e.Name, Kind: e.Kind,
		CreatedAt: unixTime(now.Unix()), ExpiresAt: unixTime(exp.Unix()), State: "active",
	}, nil
}

// Poll answers the device's poll: the device id once approved (once only), else
// ErrAuthorizationPending, ErrSlowDown or ErrExpiredToken. An unknown device code, one already
// answered and an expired one all get ErrExpiredToken.
func (s *Service) Poll(ctx context.Context, deviceCode string) (int64, error) {
	h := hashBytes([]byte(deviceCode))
	now := time.Now().Unix()
	e, err := s.rq.GetEnrollmentByDeviceCode(ctx, h)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && e.ExpiresAt <= now) {
		return 0, ErrExpiredToken
	}
	if err != nil {
		return 0, err
	}
	n, err := s.wq.TouchEnrollmentPoll(ctx, dbq.TouchEnrollmentPollParams{
		Now: now, DeviceCodeHash: h, MinPolledAt: now - int64(pollInterval/time.Second) + 1,
	})
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrSlowDown
	}
	if !e.DeviceID.Valid {
		return 0, ErrAuthorizationPending
	}
	id, err := s.wq.TakeApprovedEnrollment(ctx, h)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrExpiredToken
	}
	if err != nil {
		return 0, err
	}
	return id.Int64, nil
}

// UserDevices returns a newest-first keyset page of a user's devices: ids below after (0: first
// page). next is the cursor of the next page, 0 at the end.
func (s *Service) UserDevices(ctx context.Context, userID, after int64, limit int) ([]Device, int64, error) {
	rows, err := s.rq.ListUserDevices(ctx, dbq.ListUserDevicesParams{UserID: userID, Before: beforeCursor(after), Limit: int64(limit)})
	if err != nil {
		return nil, 0, err
	}
	now := time.Now().Unix()
	out := make([]Device, 0, len(rows))
	for _, r := range rows {
		out = append(out, Device{
			ID: r.ID, UserID: r.UserID, Name: r.Name, Kind: r.Kind,
			CreatedAt: unixTime(r.CreatedAt), ExpiresAt: unixTime(r.ExpiresAt),
			LastUsedAt: unixPtr(r.LastUsedAt), RevokedAt: unixPtr(r.RevokedAt),
			State: deviceState(r.RevokedAt, r.ExpiresAt, now),
		})
	}
	return out, nextID(len(rows), limit, func() int64 { return rows[len(rows)-1].ID }), nil
}

// Devices returns a newest-first keyset page of every device with its owner (people).
func (s *Service) Devices(ctx context.Context, after int64, limit int) ([]Device, int64, error) {
	rows, err := s.rq.ListDevices(ctx, dbq.ListDevicesParams{Before: beforeCursor(after), Limit: int64(limit)})
	if err != nil {
		return nil, 0, err
	}
	now := time.Now().Unix()
	out := make([]Device, 0, len(rows))
	for _, r := range rows {
		out = append(out, Device{
			ID: r.ID, UserID: r.UserID, Username: r.Username, Name: r.Name, Kind: r.Kind,
			CreatedAt: unixTime(r.CreatedAt), ExpiresAt: unixTime(r.ExpiresAt),
			LastUsedAt: unixPtr(r.LastUsedAt), RevokedAt: unixPtr(r.RevokedAt),
			State: deviceState(r.RevokedAt, r.ExpiresAt, now),
		})
	}
	return out, nextID(len(rows), limit, func() int64 { return rows[len(rows)-1].ID }), nil
}

// RevokeDevice revokes a device of p (any device when p holds the People permission) and ends its open
// sessions. ErrNotFound: unknown, another user's or already revoked.
func (s *Service) RevokeDevice(ctx context.Context, p *auth.Principal, id int64) error {
	now := nullID(time.Now().Unix())
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		var n int64
		var err error
		if p.Perms.Has(auth.PermPeople) {
			n, err = q.RevokeDevice(ctx, dbq.RevokeDeviceParams{RevokedAt: now, ID: id})
		} else {
			n, err = q.RevokeUserDevice(ctx, dbq.RevokeUserDeviceParams{RevokedAt: now, ID: id, UserID: p.UserID})
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return errorf(ErrNotFound, "device not found")
		}
		_, err = q.CloseDevicePublishSessions(ctx, dbq.CloseDevicePublishSessionsParams{ClosedAt: now, DeviceID: nullID(id)})
		return err
	})
}
