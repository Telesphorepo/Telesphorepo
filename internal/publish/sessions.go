package publish

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/provenance"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

const (
	nonceKind = "publish_nonce"
	nonceTTL  = 60 * time.Second
	// signedPrefix starts the message a device signs to mint a session.
	signedPrefix = "telesphorepo publish v1\n"
)

// Minted is a new publish session. Token is shown once; it is the HTTP Basic password (any
// username) for the publish, and it also reads the repo.
type Minted struct {
	ID        int64     `json:"id"`
	Token     string    `json:"token"`
	Repo      string    `json:"repo"`
	ExpiresAt time.Time `json:"expires_at"`

	// For the audit log; not in the JSON answer.
	RepoID  int64  `json:"-"`
	UserID  int64  `json:"-"` // 0 for a CI session
	Subject string `json:"-"` // the username, or the CI token's sub
	Via     string `json:"-"`
}

// SessionInfo is a publish session in a user's session list or the admin publish log. State is
// "open", "closed" or "expired".
type SessionInfo struct {
	ID         int64      `json:"id"`
	Repo       string     `json:"repo"`
	Via        string     `json:"via"`
	Subject    string     `json:"subject"`
	Username   string     `json:"username,omitempty"`
	DeviceID   *int64     `json:"device_id"`
	DeviceName string     `json:"device_name,omitempty"`
	TrustID    *int64     `json:"trust_id"`
	TrustName  string     `json:"trust_name,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	ClosedAt   *time.Time `json:"closed_at"`
	State      string     `json:"state"`
}

func unixTime(n int64) time.Time { return time.Unix(n, 0).UTC() }

func unixPtr(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := unixTime(n.Int64)
	return &t
}

func idPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

func sessionState(closed sql.NullInt64, expires, now int64) string {
	switch {
	case closed.Valid:
		return "closed"
	case expires <= now:
		return "expired"
	}
	return "open"
}

// beforeCursor turns an id cursor (0: first page) into the exclusive upper bound of a
// newest-first keyset page.
func beforeCursor(after int64) int64 {
	if after <= 0 {
		return math.MaxInt64
	}
	return after
}

// writableRepo loads the repo and checks that p may write to it now and that it takes uploads.
func (s *Service) writableRepo(ctx context.Context, p *auth.Principal, name string) (*repos.Repo, error) {
	repo, err := s.repos.ByName(ctx, name)
	if errors.Is(err, repos.ErrNotFound) {
		return nil, errorf(ErrNotFound, "repo not found")
	}
	if err != nil {
		return nil, err
	}
	lvl, err := s.auth.Access(ctx, p, repo)
	if err != nil {
		return nil, err
	}
	if lvl < auth.Write {
		return nil, errorf(ErrForbidden, "you cannot publish to "+repo.Name)
	}
	if repo.LockedBy != "" {
		return nil, errorf(ErrConflict, repo.LockedBy)
	}
	return repo, nil
}

// mint creates a session on q (a transaction or the write pool), with the build provenance of
// a CI token (nil: none). It retries on the rare collision of the 48-bit public id.
func (s *Service) mint(ctx context.Context, q *dbq.Queries, repo *repos.Repo, via string, userID, deviceID, trustID int64, subject string, prov *provenance.Record) (*Minted, error) {
	now := time.Now()
	exp := now.Add(s.sessionTTL())
	if prov == nil {
		prov = &provenance.Record{}
	}
	for range 3 {
		pid, secret, tok := newSessionToken()
		id, err := q.CreatePublishSession(ctx, dbq.CreatePublishSessionParams{
			PublicID: pid, SecretHash: hashBytes([]byte(secret)), RepoID: repo.ID,
			UserID: nullID(userID), DeviceID: nullID(deviceID), TrustID: nullID(trustID),
			Via: via, Subject: subject, CreatedAt: now.Unix(), ExpiresAt: exp.Unix(),
			CiServer: prov.Server, CiRepository: prov.Repository, CiSha: prov.SHA, CiRef: prov.Ref,
			CiRunID: prov.RunID, CiRunAttempt: prov.RunAttempt, CiWorkflow: prov.Workflow,
			CiWorkflowRef: prov.WorkflowRef, CiActor: prov.Actor,
		})
		if isUnique(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &Minted{ID: id, Token: tok, Repo: repo.Name, ExpiresAt: unixTime(exp.Unix()),
			RepoID: repo.ID, UserID: userID, Subject: subject, Via: via}, nil
	}
	return nil, errors.New("publish session id collision")
}

// usableDevice loads a device that may mint sessions: not revoked, not expired, and owned by an
// enabled local account.
func (s *Service) usableDevice(ctx context.Context, id int64, now int64) (dbq.GetDeviceRow, error) {
	d, err := s.rq.GetDevice(ctx, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return d, errorf(ErrDenied, "unknown device; enroll again")
	case err != nil:
		return d, err
	case d.RevokedAt.Valid:
		return d, errorf(ErrDenied, "device revoked; enroll again")
	case d.ExpiresAt <= now:
		return d, errorf(ErrDenied, "device expired; enroll again")
	case d.Disabled || d.UserKind != auth.KindLocal:
		return d, errorf(ErrDenied, "account disabled")
	}
	return d, nil
}

// Challenge returns a single-use nonce (60 s) that the device signs to mint a session.
func (s *Service) Challenge(ctx context.Context, deviceID int64) (string, error) {
	now := time.Now().Unix()
	if _, err := s.usableDevice(ctx, deviceID, now); err != nil {
		return "", err
	}
	nonce := randomString(32)
	err := s.wq.CreateChallenge(ctx, dbq.CreateChallengeParams{
		IDHash: hashBytes([]byte(nonce)), Kind: nonceKind, DeviceID: nullID(deviceID), Data: []byte{},
		CreatedAt: now, ExpiresAt: now + int64(nonceTTL/time.Second),
	})
	if err != nil {
		return "", err
	}
	return nonce, nil
}

// SignedMessage is what a device signs with Ed25519 to mint a session for repo.
func SignedMessage(nonce string, deviceID int64, repo string) []byte {
	return []byte(signedPrefix + nonce + "\n" + strconv.FormatInt(deviceID, 10) + "\n" + repo)
}

func decodeB64URL(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	return base64.RawURLEncoding.DecodeString(s)
}

// DeviceSession mints a session from a device: the nonce must be one the device asked for, the
// signature must verify with the device key, and the device's user must have write on the repo
// now. The nonce is used up by the call whatever its outcome.
func (s *Service) DeviceSession(ctx context.Context, ip netip.Addr, deviceID int64, repoName, nonce, signature string) (*Minted, error) {
	now := time.Now().Unix()
	ch, err := s.wq.TakeChallenge(ctx, dbq.TakeChallengeParams{IDHash: hashBytes([]byte(nonce)), Kind: nonceKind, Now: now})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && ch.DeviceID.Int64 != deviceID) {
		return nil, errorf(ErrDenied, "invalid or expired nonce")
	}
	if err != nil {
		return nil, err
	}
	d, err := s.usableDevice(ctx, deviceID, now)
	if err != nil {
		return nil, err
	}
	sig, err := decodeB64URL(signature)
	if err != nil || len(d.PublicKey) != ed25519.PublicKeySize ||
		!ed25519.Verify(ed25519.PublicKey(d.PublicKey), SignedMessage(nonce, deviceID, repoName), sig) {
		return nil, errorf(ErrDenied, "bad device signature")
	}
	p := &auth.Principal{UserID: d.UserID, Username: d.Username, IsAdmin: d.IsAdmin, Kind: d.UserKind}
	repo, err := s.writableRepo(ctx, p, repoName)
	if err != nil {
		return nil, err
	}
	if err := s.ipAllowed(repo, ip, auth.ActorOf(p)); err != nil {
		return nil, err
	}
	m, err := s.mint(ctx, s.wq, repo, ViaDevice, d.UserID, d.ID, 0, d.Username, nil)
	if err != nil {
		return nil, err
	}
	if err := s.wq.TouchDevice(ctx, dbq.TouchDeviceParams{LastUsedAt: nullID(now), ID: d.ID}); err != nil {
		s.log.WarnContext(ctx, "record device use", "device", d.ID, "error", err.Error())
	}
	return m, nil
}

// ManualSession mints a session for a signed-in user after a fresh second-factor check. The
// second-factor error comes back unchanged (from auth.Service.VerifySecondFactor).
func (s *Service) ManualSession(ctx context.Context, ip netip.Addr, p *auth.Principal, repoName, totpCode string, passkey json.RawMessage) (*Minted, error) {
	if p.Kind != auth.KindLocal {
		return nil, errorf(ErrForbidden, "publish sessions need a local account")
	}
	repo, err := s.writableRepo(ctx, p, repoName)
	if err != nil {
		return nil, err
	}
	if err := s.ipAllowed(repo, ip, auth.ActorOf(p)); err != nil {
		return nil, err
	}
	if err := s.auth.VerifySecondFactor(ctx, p.UserID, totpCode, passkey); err != nil {
		return nil, err
	}
	return s.mint(ctx, s.wq, repo, ViaManual, p.UserID, 0, 0, p.Username, nil)
}

// Closed is a session that CloseSession ended, for the audit log.
type Closed struct {
	RepoID  int64
	Repo    string
	UserID  int64 // 0 for a CI session
	Subject string
	Via     string
}

// CloseSession ends session id. token must be that session's token; a session that already
// ended stays as it is (Closed is then nil).
func (s *Service) CloseSession(ctx context.Context, id int64, token string) (*Closed, error) {
	row, err := s.lookupToken(ctx, token)
	if errors.Is(err, auth.ErrBadCredentials) || (err == nil && row.ID != id) {
		return nil, errorf(ErrDenied, "the token does not belong to this publish session")
	}
	if err != nil {
		return nil, err
	}
	n, err := s.wq.ClosePublishSession(ctx, dbq.ClosePublishSessionParams{ClosedAt: nullID(time.Now().Unix()), ID: id})
	if err != nil || n == 0 {
		return nil, err
	}
	return &Closed{RepoID: row.RepoID, Repo: row.Repo, UserID: row.UserID.Int64, Subject: row.Subject, Via: row.Via}, nil
}

// UserSessions returns a newest-first keyset page of a user's sessions: ids below after (0:
// first page). next is the cursor of the next page, 0 at the end.
func (s *Service) UserSessions(ctx context.Context, userID, after int64, limit int) ([]SessionInfo, int64, error) {
	rows, err := s.rq.ListUserPublishSessions(ctx, dbq.ListUserPublishSessionsParams{
		UserID: nullID(userID), Before: beforeCursor(after), Limit: int64(limit),
	})
	if err != nil {
		return nil, 0, err
	}
	now := time.Now().Unix()
	out := make([]SessionInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, SessionInfo{
			ID: r.ID, Repo: r.Repo, Via: r.Via, Subject: r.Subject,
			DeviceID: idPtr(r.DeviceID), DeviceName: r.DeviceName.String,
			CreatedAt: unixTime(r.CreatedAt), ExpiresAt: unixTime(r.ExpiresAt), ClosedAt: unixPtr(r.ClosedAt),
			State: sessionState(r.ClosedAt, r.ExpiresAt, now),
		})
	}
	return out, nextID(len(rows), limit, func() int64 { return rows[len(rows)-1].ID }), nil
}

// Log returns a newest-first keyset page of every session (the admin publish log).
func (s *Service) Log(ctx context.Context, after int64, limit int) ([]SessionInfo, int64, error) {
	rows, err := s.rq.ListPublishLog(ctx, dbq.ListPublishLogParams{Before: beforeCursor(after), Limit: int64(limit)})
	if err != nil {
		return nil, 0, err
	}
	now := time.Now().Unix()
	out := make([]SessionInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, SessionInfo{
			ID: r.ID, Repo: r.Repo, Via: r.Via, Subject: r.Subject, Username: r.Username.String,
			DeviceID: idPtr(r.DeviceID), DeviceName: r.DeviceName.String,
			TrustID: idPtr(r.TrustID), TrustName: r.TrustName.String,
			CreatedAt: unixTime(r.CreatedAt), ExpiresAt: unixTime(r.ExpiresAt), ClosedAt: unixPtr(r.ClosedAt),
			State: sessionState(r.ClosedAt, r.ExpiresAt, now),
		})
	}
	return out, nextID(len(rows), limit, func() int64 { return rows[len(rows)-1].ID }), nil
}

// nextID is the cursor after a full page, 0 after a short one.
func nextID(n, limit int, last func() int64) int64 {
	if n == 0 || n < limit {
		return 0
	}
	return last()
}
