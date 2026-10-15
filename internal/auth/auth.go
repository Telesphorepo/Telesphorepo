// Package auth: users, passwords, read tokens, sessions, second factors, rate limiting,
// permissions (spec §7).
package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

var (
	ErrBadCredentials = errors.New("invalid credentials")
	ErrRateLimited    = errors.New("too many failed attempts")
	ErrNotFound       = errors.New("not found")
	ErrExists         = errors.New("already exists")
	ErrInvalid        = errors.New("invalid input")
	// ErrSSOAccount: the operation needs a local account (SSO accounts have no password and no
	// API tokens).
	ErrSSOAccount = errors.New("not available for SSO accounts")
	// ErrPasswordLoginDisabled: admin_password_login is off and the user is a local admin.
	ErrPasswordLoginDisabled = errors.New("password sign-in is disabled for administrators; sign in with SSO")
	// ErrUsernameConflict: an SSO login's username belongs to a different account.
	ErrUsernameConflict = errors.New("username belongs to another account")
	// ErrDisabled: the account exists but is disabled.
	ErrDisabled = errors.New("account disabled")
	// ErrBadSecondFactor: a TOTP code, passkey assertion or recovery code did not verify. It
	// wraps ErrBadCredentials, so callers that map ErrBadCredentials answer 401.
	ErrBadSecondFactor = fmt.Errorf("%w: second factor did not verify", ErrBadCredentials)
	// ErrNoSecondFactor: the user has no TOTP factor and no passkey yet.
	ErrNoSecondFactor = errors.New("second_factor_required")
	// ErrPasskeysUnavailable: passkeys need base_url for the WebAuthn RP ID and origin.
	ErrPasskeysUnavailable = errors.New("passkeys need the public base URL (setting base_url)")
)

// Account kinds (users.kind).
const (
	KindLocal = "local"
	KindOIDC  = "oidc"
)

// noPassword is the password_hash of accounts without a password (SSO). It is not a PHC string,
// so verifyPassword never accepts it.
const noPassword = "!"

// Principal is an authenticated caller. A nil *Principal means anonymous.
type Principal struct {
	UserID   int64 // 0 for a publish session minted from a CI identity token
	Username string
	Perms    ServerPerm // the user's server permissions
	TokenID  int64      // non-zero when authenticated with a read token
	Kind     string     // KindLocal or KindOIDC; "" for a publish session without a user
	// Session is true when the caller authenticated with the UI session cookie.
	Session bool
	// ManagesRepo is true for a UI session of a user who holds a repository permission other
	// than download and publish.
	ManagesRepo bool
	// Publish is non-nil only when the caller authenticated with a publish-session token. The
	// write gate limits such a principal to the session's repo, and Identify limits it to
	// /api/publish/ on the JSON API.
	Publish *PublishSession
}

type User struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	Permissions []string   `json:"permissions"` // server permissions, in display order
	Perms       ServerPerm `json:"-"`           // the same set
	Disabled    bool       `json:"disabled"`
	Kind        string     `json:"kind"`  // "local" or "oidc"
	Email       string     `json:"email"` // "" when unknown (SSO accounts only)
	CreatedAt   time.Time  `json:"created_at"`
}

type Token struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// Permission is the repository permission set of one person on one repository.
type Permission struct {
	Username string   `json:"username"`
	Perms    []string `json:"perms"`
}

type Service struct {
	db    *db.DB
	rq    *dbq.Queries // reads, on the read pool
	wq    *dbq.Queries // writes, on the write pool
	cfg   config.Provider
	repos *repos.Service

	secret  []byte        // per-process HMAC key for the Basic verification cache
	sem     chan struct{} // bounds concurrent argon2 work
	cache   *verifyCache
	limits  *limiterSet
	dummyMu sync.Once
	dummy   string // hash verified against when the user does not exist
	touchMu sync.Mutex
	touched map[int64]int64 // token id -> unix second of last last_used_at write

	flightMu sync.Mutex
	flights  map[[32]byte]*flight // in-progress uncached password checks by cache key

	sealer  Sealer            // seals TOTP secrets; set by the server
	publish PublishTokens     // verifies publish-session tokens; set by the server
	audit   *audit.Service    // records sign-ins; set by the server (nil: none)
	rules   *netrules.Service // per-repo IP rules; set by the server (nil: none)

	// dev makes the second factor optional (telesphorepo dev only): a person without a factor
	// gets a full session, and an approval or a manual session without a factor passes. A
	// factor that the user has is still checked.
	dev bool
}

// SetAudit sets the audit log that sign-ins are recorded in.
func (s *Service) SetAudit(a *audit.Service) { s.audit = a }

// SetNetRules sets the per-repo IP rules that the repo gates apply.
func (s *Service) SetNetRules(n *netrules.Service) { s.rules = n }

// SetDevMode makes the second factor optional. Only the dev command calls it.
func (s *Service) SetDevMode(on bool) { s.dev = on }

// DevMode reports whether the second factor is optional.
func (s *Service) DevMode() bool { return s.dev }

// ActorOf is the audit actor of p: the user, or the CI identity of a publish session without a
// user. nil is anonymous.
func ActorOf(p *Principal) audit.Actor {
	switch {
	case p == nil:
		return audit.Actor{Name: "anonymous"}
	case p.Username != "":
		return audit.Actor{UserID: p.UserID, Name: p.Username}
	case p.Publish != nil:
		return audit.Actor{Name: p.Publish.Subject}
	}
	return audit.Actor{UserID: p.UserID}
}

// Sealer encrypts secrets at rest, bound to their place by aad. settings.Service implements it.
type Sealer interface {
	Encrypt(aad string, plaintext []byte) []byte
	Decrypt(aad string, sealed []byte) ([]byte, error)
}

// SetSealer sets the encryption for TOTP secrets. TOTP enrolment and checks fail without it.
func (s *Service) SetSealer(sl Sealer) { s.sealer = sl }

func New(d *db.DB, cfg config.Provider, rs *repos.Service) *Service {
	secret := make([]byte, 32)
	rand.Read(secret)
	return &Service{
		db: d, rq: dbq.New(d.R), wq: dbq.New(d.W), cfg: cfg, repos: rs,
		secret:  secret,
		sem:     make(chan struct{}, 4),
		cache:   newVerifyCache(),
		limits:  newLimiterSet(),
		touched: map[int64]int64{},
		flights: map[[32]byte]*flight{},
	}
}
