// Package publish implements publisher credentials (spec §7.6): enrolled device keys, publish
// sessions, CI trust policies with the OIDC token exchange, the publish log and the job that
// deletes ended rows. A publish session is the only credential that writes on a format route;
// the Service verifies its token for auth.Service through auth.PublishTokens.
package publish

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
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

const (
	tokenPrefix = "trs_"
	// logRetention is how long ended sessions stay in the publish log.
	logRetention = 30 * 24 * time.Hour
)

// Service owns devices, enrollments, publish nonces, publish sessions and trust policies.
type Service struct {
	db       *db.DB
	rq, wq   *dbq.Queries
	auth     *auth.Service
	repos    *repos.Service
	settings *settings.Service
	log      *slog.Logger

	enrollLimits *ipLimiter

	provMu sync.Mutex
	provs  map[string]*gooidc.Provider // discovered CI issuers; failures are not cached

	wg sync.WaitGroup
}

var _ auth.PublishTokens = (*Service)(nil)

// New returns the publish service. Register it with auth.Service.SetPublishTokens so that the
// write gate accepts its sessions, and call Start to run the cleanup job.
func New(d *db.DB, a *auth.Service, rs *repos.Service, st *settings.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		db: d, rq: dbq.New(d.R), wq: dbq.New(d.W), auth: a, repos: rs, settings: st, log: log,
		enrollLimits: newIPLimiter(),
		provs:        map[string]*gooidc.Provider{},
	}
}

func (s *Service) sessionTTL() time.Duration {
	return s.settings.Current().Duration(settings.KeyPublishSessionTTL)
}

func (s *Service) deviceTTL() time.Duration {
	return s.settings.Current().Duration(settings.KeyPublishDeviceTTL)
}

func (s *Service) baseURL() string { return s.settings.Config().BaseURL }

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

// newSessionToken returns a token "trs_<12 hex>_<43 char base64url>" and its parts.
func newSessionToken() (publicID, secret, token string) {
	publicID = hex.EncodeToString(randomBytes(6))
	secret = randomString(32)
	return publicID, secret, tokenPrefix + publicID + "_" + secret
}

// parseSessionToken splits a session token into its public id and secret.
func parseSessionToken(tok string) (publicID, secret string, ok bool) {
	rest, ok := strings.CutPrefix(tok, tokenPrefix)
	if !ok || len(rest) != 12+1+43 || rest[12] != '_' {
		return "", "", false
	}
	publicID, secret = rest[:12], rest[13:]
	for i := 0; i < len(publicID); i++ {
		if c := publicID[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", "", false
		}
	}
	return publicID, secret, true
}

// IsSessionToken reports whether tok has the shape of a publish session token.
func IsSessionToken(tok string) bool {
	_, _, ok := parseSessionToken(tok)
	return ok
}

// sessionOpen applies the rules of an open session: not closed, not expired, and the device or
// trust policy behind it still valid.
func sessionOpen(row dbq.GetPublishSessionAuthRow, now int64) bool {
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

// lookupToken finds the session of a token and checks its secret in constant time. It does
// not check that the session is open.
func (s *Service) lookupToken(ctx context.Context, token string) (dbq.GetPublishSessionAuthRow, error) {
	pid, secret, ok := parseSessionToken(token)
	if !ok {
		return dbq.GetPublishSessionAuthRow{}, auth.ErrBadCredentials
	}
	row, err := s.rq.GetPublishSessionAuth(ctx, pid)
	if errors.Is(err, sql.ErrNoRows) {
		return row, auth.ErrBadCredentials
	}
	if err != nil {
		return row, err
	}
	if subtle.ConstantTimeCompare(hashBytes([]byte(secret)), row.SecretHash) != 1 {
		return row, auth.ErrBadCredentials
	}
	return row, nil
}

// Verify implements auth.PublishTokens: it returns the open session of token, or
// auth.ErrBadCredentials when the token is unknown, closed or expired, or its device or trust
// policy is no longer valid.
func (s *Service) Verify(ctx context.Context, token string) (*auth.PublishSession, error) {
	row, err := s.lookupToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if !sessionOpen(row, time.Now().Unix()) {
		return nil, auth.ErrBadCredentials
	}
	return &auth.PublishSession{ID: row.ID, RepoID: row.RepoID, UserID: row.UserID.Int64, Via: row.Via}, nil
}
