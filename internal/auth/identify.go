package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/perm"
)

// rateLimitedError carries the Retry-After duration.
type rateLimitedError struct{ wait time.Duration }

func (e *rateLimitedError) Error() string { return ErrRateLimited.Error() }
func (e *rateLimitedError) Unwrap() error { return ErrRateLimited }

// authenticate verifies username/password (or, when allowToken, a token of a registered kind as
// the password), applying the failed-attempt rate limits. Errors: ErrBadCredentials (or an
// error that wraps it, such as ErrPublishEnded), *rateLimitedError, or an internal error.
func (s *Service) authenticate(ctx context.Context, ip netip.Addr, username, password string, allowToken bool) (*Principal, error) {
	keys := limitKeys(ip, username)
	if d := s.limits.blocked(keys...); d > 0 {
		return nil, &rateLimitedError{d}
	}
	if allowToken {
		if v, pub, sec, ok := s.tokenKind(password); ok {
			// Tokens carry 256 random bits and are checked with one SHA-256, so a plain
			// check-then-record is enough here; reserving would throttle parallel CI clients.
			p, err := v(ctx, pub, sec)
			if errors.Is(err, ErrBadCredentials) {
				s.limits.fail(keys...)
			}
			return p, err
		}
	}
	return s.verifyUserPassword(ctx, keys, username, password)
}

// flight is an in-progress uncached password verification shared by identical requests.
type flight struct {
	done chan struct{}
	err  error
}

// verifyUserPassword checks a password via the verify cache, falling back to argon2. Cache
// misses reserve an attempt on the rate-limit keys for the duration of the check, and
// concurrent requests with identical credentials wait for a single verification.
func (s *Service) verifyUserPassword(ctx context.Context, keys []string, username, password string) (*Principal, error) {
	if username == "" || password == "" {
		s.limits.fail(keys...)
		return nil, ErrBadCredentials
	}
	key := s.cacheKey(username, password)
	for {
		if uid, hash, ok := s.cache.get(key); ok {
			// The DB is the source of truth: a password changed by another process (e.g.
			// "telesphorepo user passwd") changes the stored hash and voids this entry.
			p, cur, err := s.principalAndHash(ctx, uid)
			if err != nil {
				return nil, err
			}
			if p.Username == username && cur == hash {
				return p, nil
			}
			s.cache.invalidate(uid)
			continue
		}
		s.flightMu.Lock()
		if f, ok := s.flights[key]; ok {
			s.flightMu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if errors.Is(f.err, ErrBadCredentials) {
				return nil, ErrBadCredentials // same guess; already counted once
			}
			continue // success is now cached (or retry after another error)
		}
		f := &flight{done: make(chan struct{})}
		s.flights[key] = f
		s.flightMu.Unlock()

		p, err := s.verifyUncached(ctx, keys, key, username, password)
		f.err = err
		s.flightMu.Lock()
		delete(s.flights, key)
		s.flightMu.Unlock()
		close(f.done)
		return p, err
	}
}

func (s *Service) verifyUncached(ctx context.Context, keys []string, key [32]byte, username, password string) (p *Principal, err error) {
	if d := s.limits.begin(keys...); d > 0 {
		return nil, &rateLimitedError{d}
	}
	defer func() { s.limits.end(errors.Is(err, ErrBadCredentials), keys...) }()

	u, err := s.rq.GetUserLogin(ctx, username)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && u.Kind != KindLocal) {
		// SSO accounts never authenticate with a password; same timing as an unknown user.
		s.verifyPassword(ctx, s.dummyHash(ctx), password)
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	id, hash := u.ID, u.PasswordHash
	gen := s.cache.generation(id)
	ok, err := s.verifyPassword(ctx, hash, password)
	if err != nil && !errors.Is(err, errBadHash) {
		return nil, err
	}
	if !ok || u.Disabled {
		return nil, ErrBadCredentials
	}
	// The hash was read before gen was captured, so a password change committed in between
	// would not stop the put below. Re-read it and only cache if it is unchanged; any later
	// change bumps the generation after gen was captured and so voids the entry.
	if cur, err := s.rq.GetUserPasswordHash(ctx, id); err == nil && cur == hash {
		s.cache.put(key, id, hash, gen)
	}
	return &Principal{Cred: CredPassword, UserID: id, Username: username, Perms: perm.Joined(u.Perms), Kind: u.Kind}, nil
}

// principalByID re-reads the user so permission and disabled changes apply immediately.
// principalAndHash is principalByID plus the stored password hash, in one PK lookup.
func (s *Service) principalAndHash(ctx context.Context, id int64) (*Principal, string, error) {
	u, err := s.rq.GetPrincipalAndHash(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (u.Disabled || u.Kind != KindLocal)) {
		return nil, "", ErrBadCredentials
	}
	if err != nil {
		return nil, "", err
	}
	return &Principal{Cred: CredPassword, UserID: id, Username: u.Username, Perms: perm.Joined(u.Perms), Kind: u.Kind}, u.PasswordHash, nil
}

func (s *Service) principalByID(ctx context.Context, id int64) (*Principal, error) {
	u, err := s.rq.GetPrincipal(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && u.Disabled) {
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	return &Principal{UserID: id, Username: u.Username, Perms: perm.Joined(u.Perms), Kind: u.Kind}, nil
}

// UIHeader is the header the web UI's API client sends (value "1"). A 401 to a request with it
// carries no WWW-Authenticate challenge, so a browser never opens its password dialog.
const UIHeader = "X-Telesphorepo-UI"

// ErrSessionRequired is the answer to a request that needs a UI session but came with HTTP
// Basic (a password or a read token) or with no credential.
var ErrSessionRequired = errors.New("sign in through the web UI for this action")

// Read says which credentials Identify reads besides the Authorization header (a Basic
// password, or a token of a registered kind as the Basic password or as a Bearer token).
type Read struct {
	Cookie      bool   // the tr_session cookie of a UI session
	Bearer      bool   // core bearer tokens (IssueBearer) in "Authorization: Bearer"
	TokenHeader string // a header that carries a token of a registered kind, e.g. X-NuGet-ApiKey
}

// Identify authenticates r with the credentials that rd names and returns the caller, or nil for
// a request without a credential. It decides nothing about the route: the route policy
// (internal/route) checks the kind of the credential. Errors: ErrBadCredentials (or
// ErrPublishEnded, which wraps it), a rate-limit error (RetryAfter gives the wait) or an
// internal error.
func (s *Service) Identify(r *http.Request, rd Read) (*Principal, error) {
	ctx := r.Context()
	if h := r.Header.Get("Authorization"); h != "" {
		if user, pass, ok := r.BasicAuth(); ok {
			return s.authenticate(ctx, s.clientIP(r), user, pass, true)
		}
		if len(h) >= 6 && strings.EqualFold(h[:6], "basic ") {
			return nil, ErrBadCredentials
		}
		if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
			tok := strings.TrimSpace(h[7:])
			if rd.Bearer && strings.HasPrefix(tok, bearerPrefix) {
				return s.verifyBearer(ctx, tok)
			}
			if s.isToken(tok) {
				return s.authenticate(ctx, s.clientIP(r), "", tok, true)
			}
		}
		if tok := strings.TrimSpace(h); rd.TokenHeader == "Authorization" && s.isToken(tok) {
			return s.authenticate(ctx, s.clientIP(r), "", tok, true)
		}
	}
	if rd.TokenHeader != "" && rd.TokenHeader != "Authorization" {
		if tok := strings.TrimSpace(r.Header.Get(rd.TokenHeader)); tok != "" {
			if !s.isToken(tok) {
				return nil, ErrBadCredentials
			}
			return s.authenticate(ctx, s.clientIP(r), "", tok, true)
		}
	}
	if rd.Cookie {
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			return s.sessionPrincipal(ctx, c.Value)
		}
	}
	return nil, nil
}

// isToken reports whether tok starts with the prefix of a registered token kind.
func (s *Service) isToken(tok string) bool {
	_, _, _, ok := s.tokenKind(tok)
	return ok
}
