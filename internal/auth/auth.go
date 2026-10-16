// Package auth: users, passwords, read tokens, sessions, second factors, rate limiting,
// permissions.
package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/token"
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
	// ErrPasswordLoginDisabled: signin.password_login is "publishers" and the local account holds
	// a permission that needs a second factor.
	ErrPasswordLoginDisabled = errors.New("password sign-in is off for people who manage; sign in with single sign-on")
	// ErrManaged: single sign-on manages the server permissions of the account.
	ErrManaged = errors.New("single sign-on manages the permissions of this person")
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

// Cred is the kind of credential a request carries. A set of kinds is a bit mask.
type Cred uint8

const (
	CredPassword     Cred = 1 << iota // HTTP Basic, username and password of a local account
	CredReadToken                     // trp_ token (Basic password or Bearer)
	CredSession                       // tr_session cookie of a full UI session
	CredSetupSession                  // tr_session cookie of a person who needs a second factor and has none
	CredPublish                       // publish session token (Basic password or Bearer)
	// CredDevice is an enrolled device key of a user, behind a core bearer token only (a format's
	// token exchange, e.g. the Docker credential helper). It reads what its user reads; it writes
	// only through the publish session of a bearer scope that the device minted.
	CredDevice
)

// Principal is an authenticated caller. A nil *Principal means anonymous. A principal from an
// anonymous core bearer token has Cred 0 and only Scopes.
type Principal struct {
	Cred     Cred
	UserID   int64 // 0 for a publish session minted from a CI identity token
	Username string
	Kind     string   // KindLocal or KindOIDC; "" for a publish session without a user
	Perms    perm.Set // the user's server permissions
	TokenID  int64    // CredReadToken only
	DeviceID int64    // CredDevice only
	// Publish is the open session of a CredPublish principal. The access engine limits such a
	// principal to the session's repo, and the route policies to the publish routes.
	Publish *PublishSession
	// Scopes is set when the credential came as a core bearer token (IssueBearer): the repos
	// and operations the token allows. nil for every other credential.
	Scopes []Scope
}

// Is reports whether p carries a credential of one of the kinds in c. A nil p carries none.
func (p *Principal) Is(c Cred) bool { return p != nil && p.Cred&c != 0 }

// Anonymous reports whether p carries no credential of a person or a session: nil, or an
// anonymous core bearer token.
func (p *Principal) Anonymous() bool { return p == nil || (p.Cred == 0 && p.UserID == 0) }

// PublishSession is an open publish session for one repo.
type PublishSession struct {
	ID, RepoID int64
	Via        string // "device", "oidc" or "manual"
	Subject    string // the username (device, manual) or the CI token's sub (oidc)
	// BuildID is the CI build record of the session (internal/ci); the files it writes point at
	// it. 0: none.
	BuildID int64
	// DeviceID is the device that minted the session (0: none).
	DeviceID int64
	// Expires is when the session ends at the latest.
	Expires time.Time
}

type User struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	Permissions []string `json:"permissions"` // server permission IDs, in registry order
	Perms       perm.Set `json:"-"`           // the same set
	Disabled    bool     `json:"disabled"`
	Kind        string   `json:"kind"` // "local" or "oidc"
	// PermissionsManaged is true when single sign-on manages the server permissions (an SSO
	// account while the claim mapping is set); they change only at a sign-in.
	PermissionsManaged bool      `json:"permissions_managed"`
	Email              string    `json:"email"` // "" when unknown (SSO accounts only)
	CreatedAt          time.Time `json:"created_at"`
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
	set   settings.Provider
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

	sealer Sealer     // seals TOTP secrets; set by the server
	audit  *audit.Log // records sign-ins, tokens and permission changes; set by the server

	tokens     map[token.Prefix]Verifier // the token kinds, by prefix (AddTokenKind)
	background sync.WaitGroup            // the last_used_at writes of read tokens (Wait)
	bearerKey  []byte                    // signs core bearer tokens (SetBearerKey)
	sessions   SessionLoader             // loads the publish sessions of bearer tokens
	devices    DeviceLoader              // loads the devices of bearer tokens
	sso        func() bool               // reports whether single sign-on is on (SetSSO)
	managed    func() bool               // reports whether single sign-on manages the permissions of SSO accounts (SetManaged)

	// dev makes the second factor optional (telesphorepo dev only): a person without a factor
	// gets a full session, and an approval or a manual session without a factor passes. A
	// factor that the user has is still checked.
	dev bool
}

// SetAudit sets the audit log that sign-ins are recorded in.
func (s *Service) SetAudit(a *audit.Log) { s.audit = a }

// SetSSO sets the function that reports whether single sign-on is on. The sso module sets it.
func (s *Service) SetSSO(on func() bool) { s.sso = on }

// SSOEnabled reports whether single sign-on is on.
func (s *Service) SSOEnabled() bool { return s.sso != nil && s.sso() }

// Wait waits for the background writes of the service. The server calls it before the database
// closes.
func (s *Service) Wait() { s.background.Wait() }

// SetDevMode makes the second factor optional. Only the dev command calls it.
func (s *Service) SetDevMode(on bool) { s.dev = on }

// DevMode reports whether the second factor is optional.
func (s *Service) DevMode() bool { return s.dev }

// Actor is the audit actor of p from the client address ip: the user, or the CI identity of a
// publish session without a user. nil and an anonymous bearer are anonymous.
func Actor(p *Principal, ip netip.Addr) audit.Actor {
	switch {
	case p.Anonymous():
		return audit.Actor{Kind: audit.ActorAnonymous, Name: "anonymous", IP: ip}
	case p.Username != "":
		return audit.Actor{Kind: audit.ActorUser, UserID: p.UserID, Name: p.Username, IP: ip}
	case p.Cred == CredPublish:
		return audit.Actor{Kind: audit.ActorCI, Name: p.Publish.Subject, IP: ip}
	}
	return audit.Actor{Kind: audit.ActorUser, UserID: p.UserID, IP: ip}
}

// Values of the setting signin.password_login.
const (
	PasswordLoginAll        = "all"        // every local account signs in with a password
	PasswordLoginPublishers = "publishers" // only local accounts without a permission that needs a second factor
)

// PasswordLogin is the setting signin.password_login. The sso module owns it (its group checks
// it); the sign-in reads it.
var PasswordLogin = settings.String(&settings.Def{Key: "signin.password_login", Default: PasswordLoginAll, Label: "Password sign-in",
	Options: []string{PasswordLoginAll, PasswordLoginPublishers},
	Help:    "With all, every local account signs in with a password. With publishers, a local account that holds a permission that needs a second factor (a person who manages) signs in with single sign-on only; publishers still sign in with a password. publishers needs single sign-on. To allow all again without single sign-on, set TR_SIGNIN_PASSWORD_LOGIN=all and restart."})

// SetManaged sets the rule of managed permissions: when fn reports true, single sign-on manages
// the server permissions of every SSO account (the sso module, while its claim mapping is set).
func (s *Service) SetManaged(fn func() bool) { s.managed = fn }

// Managed reports whether single sign-on manages the server permissions of an account of kind.
func (s *Service) Managed(kind string) bool {
	return kind == KindOIDC && s.managed != nil && s.managed()
}

// Verifier checks the parts of a token of one kind and returns its principal. A token that does
// not verify returns an error that wraps ErrBadCredentials. A token of a registered kind that
// does not parse reaches the verifier with empty parts, which match no row.
type Verifier func(ctx context.Context, publicID, secret string) (*Principal, error)

// AddTokenKind registers the verifier of the tokens with prefix p. Identify reads such a token
// from the Basic password and from "Authorization: Bearer". It panics on a prefix that is not
// valid or is taken; it runs before the server serves.
func (s *Service) AddTokenKind(p token.Prefix, v Verifier) {
	if !p.Valid() || s.tokens[p] != nil {
		panic(fmt.Sprintf("auth: token prefix %q is not valid or is taken", p))
	}
	s.tokens[p] = v
}

// tokenKind returns the verifier and parts of tok when tok starts with a registered prefix.
func (s *Service) tokenKind(tok string) (v Verifier, publicID, secret string, ok bool) {
	pre, _, found := strings.Cut(tok, "_")
	if !found {
		return nil, "", "", false
	}
	v = s.tokens[token.Prefix(pre)]
	if v == nil {
		return nil, "", "", false
	}
	if _, pub, sec, parsed := token.Parse(tok); parsed {
		return v, pub, sec, true
	}
	return v, "", "", true
}

// ErrPublishEnded is a publish session token that does not verify: unknown, malformed, closed,
// expired, or its device or trust policy ended. The answer is the same for all of them.
var ErrPublishEnded = fmt.Errorf("%w: publish session ended", ErrBadCredentials)

// PublishPrincipal returns the principal of the open publish session ps. A session with a user
// (userID not 0) carries that user's current name, kind and permissions, so the access engine checks
// the user's permission now; a session from a CI identity token has no user. A user who is gone
// or disabled ends the session (ErrPublishEnded).
func (s *Service) PublishPrincipal(ctx context.Context, ps *PublishSession, userID int64) (*Principal, error) {
	p := &Principal{Cred: CredPublish, UserID: userID, Publish: ps}
	if userID == 0 {
		return p, nil
	}
	u, err := s.principalByID(ctx, userID)
	if errors.Is(err, ErrBadCredentials) {
		return nil, ErrPublishEnded
	}
	if err != nil {
		return nil, err
	}
	p.Username, p.Kind, p.Perms = u.Username, u.Kind, u.Perms
	return p, nil
}

// Sealer encrypts secrets at rest, bound to their place by aad. settings.Service implements it.
type Sealer interface {
	Encrypt(aad string, plaintext []byte) []byte
	Decrypt(aad string, sealed []byte) ([]byte, error)
}

// SetSealer sets the encryption for TOTP secrets. TOTP enrolment and checks fail without it.
func (s *Service) SetSealer(sl Sealer) { s.sealer = sl }

func New(d *db.DB, set settings.Provider, rs *repos.Service) *Service {
	secret := make([]byte, 32)
	rand.Read(secret)
	return &Service{
		db: d, rq: dbq.New(d.R), wq: dbq.New(d.W), set: set, repos: rs,
		secret:  secret,
		sem:     make(chan struct{}, 4),
		cache:   newVerifyCache(),
		limits:  newLimiterSet(),
		touched: map[int64]int64{},
		flights: map[[32]byte]*flight{},
		tokens:  map[token.Prefix]Verifier{},
	}
}
