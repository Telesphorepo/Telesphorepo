package publish

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/publish/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/token"
)

const (
	nonceTTL = 60 * time.Second // a one-repo nonce
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

	Session *auth.PublishSession `json:"-"` // the open session, as the access engine sees it
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

// mint creates a session on q (a transaction or the write pool), with the build record of a CI
// token (0: none). It retries on the rare collision of the 48-bit public id.
func (s *Service) mint(ctx context.Context, q *dbq.Queries, repo *repos.Repo, via string, userID, deviceID, trustID, buildID int64, subject string) (*Minted, error) {
	now := time.Now()
	exp := now.Add(s.sessionTTL())
	for range 3 {
		pid, secret, tok := token.Mint(TokenPrefix)
		id, err := q.CreatePublishSession(ctx, dbq.CreatePublishSessionParams{
			PublicID: pid, SecretHash: token.Hash(secret), RepoID: repo.ID,
			UserID: nullID(userID), DeviceID: nullID(deviceID), TrustID: nullID(trustID), BuildID: nullID(buildID),
			Via: via, Subject: subject, CreatedAt: now.Unix(), ExpiresAt: exp.Unix(),
		})
		if isUnique(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &Minted{ID: id, Token: tok, Repo: repo.Name, ExpiresAt: unixTime(exp.Unix()),
			RepoID: repo.ID, UserID: userID, Subject: subject, Via: via,
			Session: &auth.PublishSession{ID: id, RepoID: repo.ID, Via: via, Subject: subject, BuildID: buildID}}, nil
	}
	return nil, errors.New("publish session id collision")
}

// usableDevice loads a device that may mint sessions: not revoked, not expired, and owned by an
// enabled account.
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
	case d.Disabled:
		return d, errorf(ErrDenied, "account disabled")
	}
	return d, nil
}

// Challenge returns a single-use nonce (60 s) that the device signs to mint a session for one
// repo.
func (s *Service) Challenge(ctx context.Context, deviceID int64) (string, error) {
	return s.challenge(ctx, deviceID, false)
}

// ExchangeChallenge returns a nonce for a token exchange of a format: the device signs it over
// the repo "*". The proof lives as long as a publish session (publish.session_ttl), so a client
// such as a Docker credential helper sends it on each token request of one push. Minter.Mint
// with the proof mints one session per repo and gives that session back on each later request
// while it is open (DeviceProof).
func (s *Service) ExchangeChallenge(ctx context.Context, deviceID int64) (string, error) {
	return s.challenge(ctx, deviceID, true)
}

func (s *Service) challenge(ctx context.Context, deviceID int64, exchange bool) (string, error) {
	now := time.Now().Unix()
	if _, err := s.usableDevice(ctx, deviceID, now); err != nil {
		return "", err
	}
	ttl := nonceTTL
	if exchange {
		ttl = s.sessionTTL()
	}
	nonce := randomString(32)
	err := s.wq.CreateNonce(ctx, dbq.CreateNonceParams{
		IDHash: hashBytes([]byte(nonce)), DeviceID: deviceID, Exchange: boolInt(exchange),
		ExpiresAt: now + int64(ttl/time.Second),
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

// CloseSession ends session id for p, the principal of that session's token (a token of another
// session is refused), and records publish.session.close. A session that ended meanwhile stays
// as it is.
func (s *Service) CloseSession(ctx context.Context, p *auth.Principal, id int64) error {
	if !p.Is(auth.CredPublish) || p.Publish.ID != id {
		return errorf(ErrDenied, "the token does not belong to this publish session")
	}
	n, err := s.wq.ClosePublishSession(ctx, dbq.ClosePublishSessionParams{ClosedAt: nullID(time.Now().Unix()), ID: id})
	if err != nil || n == 0 {
		return err
	}
	name := ""
	if repo, err := s.repos.ByID(ctx, p.Publish.RepoID); err == nil {
		name = repo.Name
	}
	_ = s.audit.Record(ctx, audit.Event{Action: ActionSessionClose, RepoID: p.Publish.RepoID, Repo: name,
		Target: strconv.FormatInt(id, 10), Detail: map[string]any{"via": p.Publish.Via}})
	return nil
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
