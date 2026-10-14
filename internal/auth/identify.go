package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
)

const challenge = `Basic realm="telesphorepo"`

// rateLimitedError carries the Retry-After duration.
type rateLimitedError struct{ wait time.Duration }

func (e *rateLimitedError) Error() string { return ErrRateLimited.Error() }
func (e *rateLimitedError) Unwrap() error { return ErrRateLimited }

// authenticate verifies username/password (or an API token as password when allowToken),
// applying the failed-attempt rate limits. Errors: ErrBadCredentials, *rateLimitedError, or
// an internal error.
func (s *Service) authenticate(ctx context.Context, ip netip.Addr, username, password string, allowToken bool) (*Principal, error) {
	keys := limitKeys(ip, username)
	if d := s.limits.blocked(keys...); d > 0 {
		return nil, &rateLimitedError{d}
	}
	if allowToken && strings.HasPrefix(password, tokenPrefix) {
		// Tokens carry 256 random bits and are checked with one SHA-256, so a plain
		// check-then-record is enough here; reserving would throttle parallel CI clients.
		p, err := s.verifyToken(ctx, password)
		if errors.Is(err, ErrBadCredentials) {
			s.limits.fail(keys...)
		}
		return p, err
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

	var (
		id              int64
		hash, kind      string
		admin, disabled bool
	)
	err = s.db.R.QueryRowContext(ctx,
		`SELECT id, password_hash, is_admin, disabled, kind FROM users WHERE username = ?`, username).
		Scan(&id, &hash, &admin, &disabled, &kind)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && kind != KindLocal) {
		// SSO accounts never authenticate with a password; same timing as an unknown user.
		s.verifyPassword(ctx, s.dummyHash(ctx), password)
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	gen := s.cache.generation(id)
	ok, err := s.verifyPassword(ctx, hash, password)
	if err != nil && !errors.Is(err, errBadHash) {
		return nil, err
	}
	if !ok || disabled {
		return nil, ErrBadCredentials
	}
	// The hash was read before gen was captured, so a password change committed in between
	// would not stop the put below. Re-read it and only cache if it is unchanged; any later
	// change bumps the generation after gen was captured and so voids the entry.
	var cur string
	if err := s.db.R.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, id).Scan(&cur); err == nil && cur == hash {
		s.cache.put(key, id, hash, gen)
	}
	return &Principal{UserID: id, Username: username, IsAdmin: admin, Kind: kind}, nil
}

// principalByID re-reads the user so admin/disabled changes apply immediately.
// principalAndHash is principalByID plus the stored password hash, in one PK lookup.
func (s *Service) principalAndHash(ctx context.Context, id int64) (*Principal, string, error) {
	p := &Principal{UserID: id}
	var (
		disabled bool
		hash     string
	)
	err := s.db.R.QueryRowContext(ctx, `SELECT username, is_admin, disabled, password_hash, kind FROM users WHERE id = ?`, id).
		Scan(&p.Username, &p.IsAdmin, &disabled, &hash, &p.Kind)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (disabled || p.Kind != KindLocal)) {
		return nil, "", ErrBadCredentials
	}
	if err != nil {
		return nil, "", err
	}
	return p, hash, nil
}

func (s *Service) principalByID(ctx context.Context, id int64) (*Principal, error) {
	p := &Principal{UserID: id}
	var disabled bool
	err := s.db.R.QueryRowContext(ctx, `SELECT username, is_admin, disabled, kind FROM users WHERE id = ?`, id).
		Scan(&p.Username, &p.IsAdmin, &disabled, &p.Kind)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && disabled) {
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", challenge)
	httpx.Error(w, http.StatusUnauthorized, msg)
}

// writeAuthError maps an authenticate error to a response.
func writeAuthError(w http.ResponseWriter, err error) {
	var rl *rateLimitedError
	switch {
	case errors.As(err, &rl):
		setRetryAfter(w, rl.wait)
		httpx.Error(w, http.StatusTooManyRequests, ErrRateLimited.Error())
	case errors.Is(err, ErrBadCredentials):
		unauthorized(w, ErrBadCredentials.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, "internal error")
	}
}

func (s *Service) Identify(w http.ResponseWriter, r *http.Request, allowSession bool) (*Principal, bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		user, pass, ok := r.BasicAuth()
		if !ok {
			if len(h) >= 6 && strings.EqualFold(h[:6], "basic ") {
				unauthorized(w, ErrBadCredentials.Error())
				return nil, false
			}
		} else {
			p, err := s.authenticate(r.Context(), s.clientIP(r), user, pass, true)
			if err != nil {
				writeAuthError(w, err)
				return nil, false
			}
			return p, true
		}
	}
	if allowSession {
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			p, err := s.sessionPrincipal(r.Context(), c.Value)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "internal error")
				return nil, false
			}
			return p, true
		}
	}
	return nil, true
}
