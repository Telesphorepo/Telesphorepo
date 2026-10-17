package publish

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/publish/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Minter turns a credential into a publish session. It is the one way to do so: the three mint
// routes use it, and a format with its own token exchange (e.g. an OCI registry) requires the
// publish module and calls module.Use[publish.Minter].
type Minter interface {
	// Mint runs access.Engine.Decide with Mint for the caller of cred on repo, writes the session
	// row and the publish.session.create event, and returns the open session. It returns no
	// token: the caller of a token exchange carries the session in its own token.
	Mint(ctx context.Context, r *http.Request, cred MintCred, repo *repos.Repo) (*auth.PublishSession, error)
	// Check verifies cred without a repo and mints nothing: a CI token that an enabled trust
	// policy of its issuer allows (audience, claims, iat, exp), or a device proof of a token
	// exchange nonce (nonce, signature, device usable, user enabled). It counts no replay.
	Check(ctx context.Context, r *http.Request, cred MintCred) error
}

// Check implements Minter.
func (s *Service) Check(ctx context.Context, r *http.Request, cred MintCred) error {
	switch {
	case cred.CIToken != "":
		return s.checkCI(ctx, cred.CIToken)
	case cred.Device != nil:
		_, err := s.checkExchangeProof(ctx, cred.Device)
		return err
	}
	return errorf(ErrInvalid, "no credential to check")
}

// checkExchangeProof verifies a device proof over a token exchange nonce (repo "*") and returns
// the device: the nonce is one the device asked for and has not expired, the device is usable,
// and the signature verifies with its key. The nonce stays.
func (s *Service) checkExchangeProof(ctx context.Context, proof *DeviceProof) (dbq.GetDeviceRow, error) {
	now := time.Now().Unix()
	if proof.Repo != "*" {
		return dbq.GetDeviceRow{}, errorf(ErrDenied, "invalid or expired nonce")
	}
	deviceID, err := s.rq.GetExchangeNonce(ctx, dbq.GetExchangeNonceParams{IDHash: hashBytes([]byte(proof.Nonce)), Now: now})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && deviceID != proof.DeviceID) {
		return dbq.GetDeviceRow{}, errorf(ErrDenied, "invalid or expired nonce")
	}
	if err != nil {
		return dbq.GetDeviceRow{}, err
	}
	d, err := s.usableDevice(ctx, proof.DeviceID, now)
	if err != nil {
		return d, err
	}
	sig, err := decodeB64URL(proof.Signature)
	if err != nil || len(d.PublicKey) != ed25519.PublicKeySize ||
		!ed25519.Verify(ed25519.PublicKey(d.PublicKey), SignedMessage(proof.Nonce, proof.DeviceID, proof.Repo), sig) {
		return d, errorf(ErrDenied, "bad device signature")
	}
	return d, nil
}

// MintCred is the credential of a mint; exactly one field is set.
type MintCred struct {
	// Device is a device signature over a nonce of Challenge (Repo is the repo), or over a nonce
	// of ExchangeChallenge (Repo is "*"). An exchange proof mints for any repo until the nonce
	// expires, and the same nonce and repo get the same open session back.
	Device *DeviceProof
	// CIToken is a CI identity token (JWT) that a trust policy of the repo allows. A token that
	// minted a session for the repo before gets that session back while it is open.
	CIToken string
	// Revision goes with CIToken: the commit that the CI job names from its env (Jenkins
	// GIT_COMMIT). The build record keeps it when the token names no commit.
	Revision string
	// Fresh is a UI session whose request carried a fresh second factor (route policy
	// FreshFactor).
	Fresh *auth.Principal
}

// DeviceProof is a device signature: Signature signs SignedMessage(Nonce, DeviceID, Repo).
type DeviceProof struct {
	DeviceID  int64
	Nonce     string
	Signature string
	Repo      string
}

// RefusedError is a mint that access.Engine.Decide refused; D is its decision.
type RefusedError struct{ D access.Decision }

func (e *RefusedError) Error() string { return e.D.Message }

// Mint implements Minter.
func (s *Service) Mint(ctx context.Context, r *http.Request, cred MintCred, repo *repos.Repo) (*auth.PublishSession, error) {
	m, err := s.mintWith(ctx, r, cred, repo.Name, func(context.Context) (*repos.Repo, error) { return repo, nil }, true)
	if err != nil {
		return nil, err
	}
	return m.Session, nil
}

// MintRoute is the mint of the routes POST /api/publish/sessions, /manual and /oidc: the repo
// comes by name, and a CI token mints once.
func (s *Service) MintRoute(ctx context.Context, r *http.Request, cred MintCred, repoName string) (*Minted, error) {
	return s.mintWith(ctx, r, cred, repoName, func(ctx context.Context) (*repos.Repo, error) { return s.repos.ByName(ctx, repoName) }, false)
}

// Exchange is the mint of POST /api/publish/sessions/exchange: a device proof of a token exchange
// nonce mints one session per repo, and a later call with the same nonce and repo gets that
// session and its token again while it is open.
func (s *Service) Exchange(ctx context.Context, r *http.Request, proof *DeviceProof, repoName string) (*Minted, error) {
	return s.mintWith(ctx, r, MintCred{Device: proof}, repoName, func(ctx context.Context) (*repos.Repo, error) { return s.repos.ByName(ctx, repoName) }, true)
}

// ParseDeviceProof reads a device proof "trx.<device id>.<nonce>.<signature>"
// (auth.DeviceProofPrefix): the signature of the device over a token exchange nonce and the repo
// "*" (SignedMessage). nil: it does not parse.
func ParseDeviceProof(s string) *DeviceProof {
	if !strings.HasPrefix(s, auth.DeviceProofPrefix) {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(s, auth.DeviceProofPrefix), ".")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return nil
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return &DeviceProof{DeviceID: id, Nonce: parts[1], Signature: parts[2], Repo: "*"}
}

// VerifyDeviceProof is the auth.DeviceProofVerifier of format reads: the CredDevice principal of
// a device proof that verifies (checkExchangeProof), or auth.ErrBadCredentials.
func (s *Service) VerifyDeviceProof(ctx context.Context, proof string) (*auth.Principal, error) {
	dp := ParseDeviceProof(proof)
	if dp == nil {
		return nil, auth.ErrBadCredentials
	}
	if _, err := s.checkExchangeProof(ctx, dp); err != nil {
		if errors.Is(err, ErrDenied) {
			return nil, auth.ErrBadCredentials
		}
		return nil, err
	}
	return s.DevicePrincipal(ctx, dp.DeviceID)
}

// mintWith checks cred first, then resolves the repo, so a caller without a valid credential
// learns nothing about which repos exist. reuse (Minter.Mint) gives a CI token its earlier
// session back and accepts a token exchange nonce; the mint routes take neither.
func (s *Service) mintWith(ctx context.Context, r *http.Request, cred MintCred, repoName string, resolve func(context.Context) (*repos.Repo, error), reuse bool) (*Minted, error) {
	var m *Minted
	var err error
	switch {
	case cred.Device != nil:
		m, err = s.deviceSession(ctx, r, cred.Device, repoName, resolve, reuse)
	case cred.CIToken != "":
		m, err = s.ciSession(ctx, r, cred.CIToken, cred.Revision, repoName, resolve, reuse)
	case cred.Fresh != nil:
		m, err = s.manualSession(ctx, r, cred.Fresh, resolve)
	default:
		return nil, errorf(ErrInvalid, "no credential to mint a publish session with")
	}
	if err != nil {
		return nil, err
	}
	if !m.Reused {
		s.recordMint(ctx, m)
	}
	return m, nil
}

// resolveRepo maps a missing repo to ErrNotFound.
func resolveRepo(ctx context.Context, resolve func(context.Context) (*repos.Repo, error)) (*repos.Repo, error) {
	repo, err := resolve(ctx)
	if errors.Is(err, repos.ErrNotFound) {
		return nil, errorf(ErrNotFound, "repo not found")
	}
	return repo, err
}

// deviceSession mints from a device: the nonce must be one the device asked for, the signature
// must verify with the device key, and the device's user must pass Decide with Mint now. A
// one-repo nonce is used up by the call whatever its outcome. A token exchange nonce (repo "*")
// works only through Minter.Mint (exchange): it stays until it expires, and each repo gets one
// session from it (exchangeSession).
func (s *Service) deviceSession(ctx context.Context, r *http.Request, proof *DeviceProof, repoName string, resolve func(context.Context) (*repos.Repo, error), exchange bool) (*Minted, error) {
	now := time.Now().Unix()
	key := hashBytes([]byte(proof.Nonce))
	var deviceID int64
	var err error
	switch {
	case proof.Repo == "*" && exchange:
		deviceID, err = s.rq.GetExchangeNonce(ctx, dbq.GetExchangeNonceParams{IDHash: key, Now: now})
	case proof.Repo == "*":
		err = sql.ErrNoRows
	default:
		deviceID, err = s.wq.TakeNonce(ctx, dbq.TakeNonceParams{IDHash: key, Now: now})
	}
	if errors.Is(err, sql.ErrNoRows) || (err == nil && deviceID != proof.DeviceID) {
		return nil, errorf(ErrDenied, "invalid or expired nonce")
	}
	if err != nil {
		return nil, err
	}
	if proof.Repo != "*" && proof.Repo != repoName {
		return nil, errorf(ErrDenied, "bad device signature")
	}
	d, err := s.usableDevice(ctx, proof.DeviceID, now)
	if err != nil {
		return nil, err
	}
	sig, err := decodeB64URL(proof.Signature)
	if err != nil || len(d.PublicKey) != ed25519.PublicKeySize ||
		!ed25519.Verify(ed25519.PublicKey(d.PublicKey), SignedMessage(proof.Nonce, proof.DeviceID, proof.Repo), sig) {
		return nil, errorf(ErrDenied, "bad device signature")
	}
	repo, err := resolveRepo(ctx, resolve)
	if err != nil {
		return nil, err
	}
	p := &auth.Principal{UserID: d.UserID, Username: d.Username, Perms: perm.Joined(d.Perms), Kind: d.UserKind}
	if dec := s.access.Decide(ctx, r, p, repo, access.Mint, perm.RepoPublish); !dec.Allowed {
		return nil, &RefusedError{dec}
	}
	var m *Minted
	if proof.Repo == "*" {
		m, err = s.exchangeSession(ctx, proof.Nonce, repo, d)
	} else {
		m, err = s.mint(ctx, s.wq, repo, ViaDevice, d.UserID, d.ID, 0, 0, d.Username)
	}
	if err != nil {
		return nil, err
	}
	if err := s.wq.TouchDevice(ctx, dbq.TouchDeviceParams{LastUsedAt: nullID(now), ID: d.ID}); err != nil {
		s.log.WarnContext(ctx, "record device use", "device", d.ID, "error", err.Error())
	}
	return m, nil
}

// exchangeSession returns the session of repo that the token exchange nonce minted, with its
// token (exchangeSecret), or mints one and binds it to the nonce. A bound session that ended is
// not replaced: the device asks for a new nonce.
func (s *Service) exchangeSession(ctx context.Context, nonce string, repo *repos.Repo, d dbq.GetDeviceRow) (*Minted, error) {
	key := hashBytes([]byte(nonce))
	var out *Minted
	var bound dbq.GetExchangeSessionRow
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		row, err := q.GetExchangeSession(ctx, dbq.GetExchangeSessionParams{NonceHash: key, RepoID: repo.ID})
		if err == nil {
			bound = row
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if out, err = s.mintKeyed(ctx, q, repo, ViaDevice, d.UserID, d.ID, 0, 0, d.Username, nonce); err != nil {
			return err
		}
		return q.InsertExchangeSession(ctx, dbq.InsertExchangeSessionParams{NonceHash: key, RepoID: repo.ID, SessionID: out.ID})
	})
	if err != nil || out != nil {
		return out, err
	}
	p, err := s.LoadSession(ctx, bound.SessionID)
	if errors.Is(err, auth.ErrBadCredentials) {
		return nil, errorf(ErrDenied, "the publish session of this device credential ended; ask for a new nonce")
	}
	if err != nil {
		return nil, err
	}
	return &Minted{ID: p.Publish.ID, Token: string(TokenPrefix) + "_" + bound.PublicID + "_" + exchangeSecret(nonce, bound.PublicID),
		Repo: repo.Name, Format: repo.Format, Kind: repo.Kind, ExpiresAt: p.Publish.Expires,
		RepoID: repo.ID, UserID: d.UserID, Subject: p.Publish.Subject, Via: ViaDevice, Reused: true, Session: p.Publish}, nil
}

// manualSession mints for a signed-in user whose request carried a fresh second factor.
func (s *Service) manualSession(ctx context.Context, r *http.Request, p *auth.Principal, resolve func(context.Context) (*repos.Repo, error)) (*Minted, error) {
	if p.Kind != auth.KindLocal {
		return nil, errorf(ErrForbidden, "publish sessions need a local account")
	}
	repo, err := resolveRepo(ctx, resolve)
	if err != nil {
		return nil, err
	}
	if d := s.access.Decide(ctx, r, p, repo, access.Mint, perm.RepoPublish); !d.Allowed {
		return nil, &RefusedError{d}
	}
	return s.mint(ctx, s.wq, repo, ViaManual, p.UserID, 0, 0, 0, p.Username)
}
