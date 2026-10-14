// Package auth: users, passwords, API tokens, sessions, rate limiting, permissions (spec §7).
package auth

import (
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
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
)

// Account kinds (users.kind).
const (
	KindLocal = "local"
	KindOIDC  = "oidc"
)

// noPassword is the password_hash of accounts without a password (SSO). It is not a PHC string,
// so verifyPassword never accepts it.
const noPassword = "!"

type Level int

const (
	None Level = iota
	Read
	Write
)

func (l Level) String() string {
	switch l {
	case Read:
		return "read"
	case Write:
		return "write"
	}
	return "none"
}

// Principal is an authenticated caller. A nil *Principal means anonymous.
type Principal struct {
	UserID   int64
	Username string
	IsAdmin  bool
	TokenID  int64  // non-zero when authenticated with an API token
	Kind     string // KindLocal or KindOIDC
}

type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	IsAdmin   bool      `json:"is_admin"`
	Disabled  bool      `json:"disabled"`
	Kind      string    `json:"kind"`  // "local" or "oidc"
	Email     string    `json:"email"` // "" when unknown (SSO accounts only)
	CreatedAt time.Time `json:"created_at"`
}

type Token struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type Permission struct {
	Username string `json:"username"`
	Level    string `json:"level"`
}

type Service struct {
	db    *db.DB
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
}

func New(d *db.DB, cfg config.Provider, rs *repos.Service) *Service {
	secret := make([]byte, 32)
	rand.Read(secret)
	return &Service{
		db: d, cfg: cfg, repos: rs,
		secret:  secret,
		sem:     make(chan struct{}, 4),
		cache:   newVerifyCache(),
		limits:  newLimiterSet(),
		touched: map[int64]int64{},
		flights: map[[32]byte]*flight{},
	}
}
