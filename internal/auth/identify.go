package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"telesphorepo/internal/httpx"
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
	var p *Principal
	var err error
	if allowToken && strings.HasPrefix(password, tokenPrefix) {
		p, err = s.verifyToken(ctx, password)
	} else {
		p, err = s.verifyUserPassword(ctx, username, password)
	}
	if errors.Is(err, ErrBadCredentials) {
		s.limits.fail(keys...)
	}
	return p, err
}

func (s *Service) verifyUserPassword(ctx context.Context, username, password string) (*Principal, error) {
	if username == "" || password == "" {
		return nil, ErrBadCredentials
	}
	key := s.cacheKey(username, password)
	if uid, ok := s.cache.get(key); ok {
		p, err := s.principalByID(ctx, uid)
		if err != nil || p.Username != username {
			if err == nil {
				err = ErrBadCredentials
			}
			return nil, err
		}
		return p, nil
	}

	var (
		id              int64
		hash            string
		admin, disabled bool
	)
	err := s.db.R.QueryRowContext(ctx,
		`SELECT id, password_hash, is_admin, disabled FROM users WHERE username = ?`, username).
		Scan(&id, &hash, &admin, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
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
	s.cache.put(key, id, gen)
	return &Principal{UserID: id, Username: username, IsAdmin: admin}, nil
}

// principalByID re-reads the user so admin/disabled changes apply immediately.
func (s *Service) principalByID(ctx context.Context, id int64) (*Principal, error) {
	p := &Principal{UserID: id}
	var disabled bool
	err := s.db.R.QueryRowContext(ctx, `SELECT username, is_admin, disabled FROM users WHERE id = ?`, id).
		Scan(&p.Username, &p.IsAdmin, &disabled)
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
