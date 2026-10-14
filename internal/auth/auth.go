// Package auth: users, passwords, API tokens, sessions, rate limiting, permissions (spec §7).
package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"telesphorepo/internal/config"
	"telesphorepo/internal/db"
	"telesphorepo/internal/repos"
)

var (
	ErrBadCredentials = errors.New("invalid credentials")
	ErrRateLimited    = errors.New("too many failed attempts")
	ErrNotFound       = errors.New("not found")
	ErrExists         = errors.New("already exists")
	ErrInvalid        = errors.New("invalid input")
)

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
	TokenID  int64 // non-zero when authenticated with an API token
}

type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	IsAdmin   bool      `json:"is_admin"`
	Disabled  bool      `json:"disabled"`
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
	cfg   *config.Config
	repos *repos.Service
	// implementation fields (caches, limiters, semaphore, process secret) go here
}

func New(d *db.DB, cfg *config.Config, rs *repos.Service) *Service { panic("TODO") }

// --- users ---

// CreateUser validates username against repos.NameRE and password length >= 8.
func (s *Service) CreateUser(ctx context.Context, username, password string, admin bool) (*User, error) {
	panic("TODO")
}
func (s *Service) GetUser(ctx context.Context, username string) (*User, error) { panic("TODO") }

// ListUsers is keyset-paginated by username.
func (s *Service) ListUsers(ctx context.Context, after string, limit int) ([]*User, string, error) {
	panic("TODO")
}

// UpdateUser changes non-nil fields; password change bumps password_changed_at, deletes the
// user's sessions and drops cached Basic verifications.
func (s *Service) UpdateUser(ctx context.Context, username string, password *string, admin, disabled *bool) (*User, error) {
	panic("TODO")
}

// HasUsers reports whether any user exists (bootstrap).
func (s *Service) HasUsers(ctx context.Context) (bool, error) { panic("TODO") }

// --- tokens ---

// CreateToken returns the stored token and the plaintext "trp_<id>_<secret>" (shown once).
// expiresIn == 0 means no expiry.
func (s *Service) CreateToken(ctx context.Context, userID int64, name string, expiresIn time.Duration) (*Token, string, error) {
	panic("TODO")
}
func (s *Service) ListTokens(ctx context.Context, userID int64) ([]*Token, error) { panic("TODO") }

// DeleteToken deletes token id if owned by p (or p is admin); ErrNotFound otherwise.
func (s *Service) DeleteToken(ctx context.Context, p *Principal, id int64) error { panic("TODO") }

// --- sessions ---

// Login verifies the password (rate limited), creates a session and sets the cookie.
func (s *Service) Login(w http.ResponseWriter, r *http.Request, username, password string) (*User, error) {
	panic("TODO")
}

// Logout deletes the caller's session and clears the cookie.
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) error { panic("TODO") }

// --- identification & authorization ---

// Identify authenticates the request via HTTP Basic (password or token), or the session
// cookie when allowSession. Returns (nil, true) for anonymous requests. On bad credentials
// it writes 401 (+ WWW-Authenticate: Basic realm="telesphorepo") or 429 and returns false.
func (s *Service) Identify(w http.ResponseWriter, r *http.Request, allowSession bool) (*Principal, bool) {
	panic("TODO")
}

// Access computes the caller's level on repo (admin=Write, permission row, anonymous_read=Read).
func (s *Service) Access(ctx context.Context, p *Principal, repo *repos.Repo) (Level, error) {
	panic("TODO")
}

// RepoGate implements the §7.3 decision table for a request on repo `name` of `format`
// (format "" = any). On failure it writes 401/403/404/429 and returns ok=false.
func (s *Service) RepoGate(w http.ResponseWriter, r *http.Request, name, format string, need Level, allowSession bool) (*repos.Repo, *Principal, bool) {
	panic("TODO")
}

// ReadableRepoIDs returns repo ids p may read; nil (and isAll=true) for admins.
func (s *Service) ReadableRepoIDs(ctx context.Context, p *Principal) (ids []int64, isAll bool, err error) {
	panic("TODO")
}

// --- permissions ---

func (s *Service) SetPermission(ctx context.Context, repoID int64, username string, level Level) error {
	panic("TODO")
}
func (s *Service) DeletePermission(ctx context.Context, repoID int64, username string) error {
	panic("TODO")
}
func (s *Service) ListPermissions(ctx context.Context, repoID int64) ([]Permission, error) {
	panic("TODO")
}
