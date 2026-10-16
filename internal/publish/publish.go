// Package publish is the publish module: publisher credentials: enrolled device
// keys, publish sessions, CI trust policies with the OIDC token exchange, the publish log and the
// job that deletes ended rows. A publish session is the only credential that writes on a format
// route; the module registers the token kind of its sessions (TokenPrefix) with the kernel.
package publish

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/clients"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/publish/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/token"
)

// Error kinds. Every error the Service returns for a caller mistake wraps one of these; its
// message is safe to show to the caller.
var (
	ErrInvalid   = errors.New("invalid input")
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("conflict")
	ErrDenied    = errors.New("not authorized") // a bad or unusable credential: 401
	ErrForbidden = errors.New("forbidden")      // a valid caller without the right: 403
	ErrLimited   = errors.New("too many requests")

	// Device flow answers of the poll (RFC 8628 §3.5), each with status 400.
	ErrAuthorizationPending = errors.New("authorization_pending")
	ErrSlowDown             = errors.New("slow_down")
	ErrExpiredToken         = errors.New("expired_token")
)

// kindError is an error of one of the kinds above with a message for the caller.
type kindError struct {
	kind error
	msg  string
}

func (e *kindError) Error() string { return e.msg }
func (e *kindError) Unwrap() error { return e.kind }

func errorf(kind error, msg string) error { return &kindError{kind, msg} }

// Session sources (publish_sessions.via).
const (
	ViaDevice = "device"
	ViaOIDC   = "oidc"
	ViaManual = "manual"
)

// TokenPrefix is the prefix of a publish session token.
const TokenPrefix token.Prefix = "trs"

// logRetention is how long ended sessions stay in the publish log.
const logRetention = 30 * 24 * time.Hour

// Service owns devices, enrollments, publish nonces, publish sessions and trust policies.
type Service struct {
	db       *db.DB
	rq, wq   *dbq.Queries
	auth     *auth.Service
	repos    *repos.Service
	settings *settings.Service
	log      *slog.Logger
	access   *access.Engine // decides every mint (access.Mint)
	audit    *audit.Log
	ci       *ci.Registry      // reads CI tokens and trust policies
	builds   *ci.Builds        // the build records of CI sessions
	clients  *clients.Registry // the publisher clients that enroll devices

	enrollLimits *ipLimiter

	provMu sync.Mutex
	provs  map[string]*gooidc.Provider // discovered CI issuers; failures are not cached
}

// Deps are the kernel services of the publish service.
type Deps struct {
	DB       *db.DB
	Auth     *auth.Service
	Access   *access.Engine // decides each mint
	Repos    *repos.Service
	Settings *settings.Service
	Audit    *audit.Log
	CI       *ci.Registry
	Builds   *ci.Builds
	Clients  *clients.Registry
	Log      *slog.Logger
}

// New returns the publish service.
func New(d Deps) *Service {
	return &Service{
		db: d.DB, rq: dbq.New(d.DB.R), wq: dbq.New(d.DB.W), auth: d.Auth, access: d.Access, audit: d.Audit,
		repos: d.Repos, settings: d.Settings, log: d.Log, ci: d.CI, builds: d.Builds, clients: d.Clients,
		enrollLimits: newIPLimiter(),
		provs:        map[string]*gooidc.Provider{},
	}
}

func (s *Service) sessionTTL() time.Duration {
	return SessionTTL.Get(s.settings.Current())
}

func (s *Service) deviceTTL() time.Duration {
	return DeviceTTL.Get(s.settings.Current())
}

func (s *Service) baseURL() string { return settings.BaseURL.Get(s.settings.Current()) }

func hashBytes(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b) // never fails (Go 1.24+)
	return b
}

func randomString(n int) string { return base64.RawURLEncoding.EncodeToString(randomBytes(n)) }

func nullID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id != 0} }

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// open is what sessionOpen reads of a session row.
type open struct {
	ClosedAt        sql.NullInt64
	ExpiresAt       int64
	Via             string
	UserID          sql.NullInt64
	DeviceID        sql.NullInt64
	DeviceRevokedAt sql.NullInt64
	DeviceExpiresAt sql.NullInt64
	TrustID         sql.NullInt64
	TrustDisabled   sql.NullInt64
}

// sessionOpen applies the rules of an open session: not closed, not expired, and the device or
// trust policy behind it still valid.
func sessionOpen(row open, now int64) bool {
	if row.ClosedAt.Valid || row.ExpiresAt <= now {
		return false
	}
	switch row.Via {
	case ViaDevice:
		return row.UserID.Valid && row.DeviceID.Valid && !row.DeviceRevokedAt.Valid &&
			row.DeviceExpiresAt.Valid && row.DeviceExpiresAt.Int64 > now
	case ViaManual:
		return row.UserID.Valid
	case ViaOIDC:
		return row.TrustID.Valid && row.TrustDisabled.Valid && row.TrustDisabled.Int64 == 0
	}
	return false
}

// lookup finds the session of a token's parts and checks its secret in constant time. It does
// not check that the session is open.
func (s *Service) lookup(ctx context.Context, publicID, secret string) (dbq.GetPublishSessionAuthRow, error) {
	if publicID == "" {
		return dbq.GetPublishSessionAuthRow{}, auth.ErrBadCredentials
	}
	row, err := s.rq.GetPublishSessionAuth(ctx, publicID)
	if errors.Is(err, sql.ErrNoRows) {
		return row, auth.ErrBadCredentials
	}
	if err != nil {
		return row, err
	}
	if !token.Check(row.SecretHash, secret) {
		return row, auth.ErrBadCredentials
	}
	return row, nil
}

// Verify is the auth.Verifier of publish session tokens: it returns the principal of the open
// session, or auth.ErrPublishEnded when the token is unknown, closed or expired, or its device,
// trust policy or user is no longer valid.
func (s *Service) Verify(ctx context.Context, publicID, secret string) (*auth.Principal, error) {
	row, err := s.lookup(ctx, publicID, secret)
	if errors.Is(err, auth.ErrBadCredentials) {
		return nil, auth.ErrPublishEnded
	}
	if err != nil {
		return nil, err
	}
	if !sessionOpen(open{row.ClosedAt, row.ExpiresAt, row.Via, row.UserID, row.DeviceID, row.DeviceRevokedAt,
		row.DeviceExpiresAt, row.TrustID, row.TrustDisabled}, time.Now().Unix()) {
		return nil, auth.ErrPublishEnded
	}
	ps := &auth.PublishSession{ID: row.ID, RepoID: row.RepoID, Via: row.Via, Subject: row.Subject, BuildID: row.BuildID.Int64}
	return s.auth.PublishPrincipal(ctx, ps, row.UserID.Int64)
}

// LoadSession is the auth.SessionLoader of core bearer tokens: the principal of open session id,
// or auth.ErrPublishEnded.
func (s *Service) LoadSession(ctx context.Context, id int64) (*auth.Principal, error) {
	row, err := s.rq.GetPublishSessionAuthByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, auth.ErrPublishEnded
	}
	if err != nil {
		return nil, err
	}
	if !sessionOpen(open{row.ClosedAt, row.ExpiresAt, row.Via, row.UserID, row.DeviceID, row.DeviceRevokedAt,
		row.DeviceExpiresAt, row.TrustID, row.TrustDisabled}, time.Now().Unix()) {
		return nil, auth.ErrPublishEnded
	}
	ps := &auth.PublishSession{ID: row.ID, RepoID: row.RepoID, Via: row.Via, Subject: row.Subject, BuildID: row.BuildID.Int64}
	return s.auth.PublishPrincipal(ctx, ps, row.UserID.Int64)
}

// recordMint writes publish.session.create of a new session. The actor is the person of the
// session, or its CI identity, from the client address of ctx.
func (s *Service) recordMint(ctx context.Context, mt *Minted) {
	kind := audit.ActorUser
	if mt.UserID == 0 {
		kind = audit.ActorCI
	}
	ctx = audit.WithActor(ctx, audit.Actor{Kind: kind, UserID: mt.UserID, Name: mt.Subject, IP: audit.ActorOf(ctx).IP})
	_ = s.audit.Record(ctx, audit.Event{
		Action: ActionSessionCreate,
		RepoID: mt.RepoID, Repo: mt.Repo, Target: strconv.FormatInt(mt.ID, 10), Detail: map[string]any{"via": mt.Via},
	})
}
