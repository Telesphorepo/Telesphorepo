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

// errPublishEnded is a failed publish-session token: unknown, malformed, closed, expired, or its
// device or trust policy ended. The response is the same for all of them.
var errPublishEnded = errors.New("publish session ended")

// rateLimitedError carries the Retry-After duration.
type rateLimitedError struct{ wait time.Duration }

func (e *rateLimitedError) Error() string { return ErrRateLimited.Error() }
func (e *rateLimitedError) Unwrap() error { return ErrRateLimited }

// authenticate verifies username/password (or a read token or publish-session token as
// password when allowToken), applying the failed-attempt rate limits. Errors:
// ErrBadCredentials, *rateLimitedError, or an internal error.
func (s *Service) authenticate(ctx context.Context, ip netip.Addr, username, password string, allowToken bool) (*Principal, error) {
	keys := limitKeys(ip, username)
	if d := s.limits.blocked(keys...); d > 0 {
		return nil, &rateLimitedError{d}
	}
	if allowToken && (strings.HasPrefix(password, tokenPrefix) || strings.HasPrefix(password, publishTokenPrefix)) {
		// Tokens carry 256 random bits and are checked with one SHA-256, so a plain
		// check-then-record is enough here; reserving would throttle parallel CI clients.
		var p *Principal
		var err error
		if strings.HasPrefix(password, publishTokenPrefix) {
			p, err = s.verifyPublishToken(ctx, password)
		} else {
			p, err = s.verifyToken(ctx, password)
		}
		if errors.Is(err, ErrBadCredentials) {
			s.limits.fail(keys...)
			if strings.HasPrefix(password, publishTokenPrefix) {
				return nil, errPublishEnded
			}
			return nil, ErrBadCredentials
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
	return &Principal{UserID: id, Username: username, IsAdmin: u.IsAdmin, Kind: u.Kind}, nil
}

// principalByID re-reads the user so admin/disabled changes apply immediately.
// principalAndHash is principalByID plus the stored password hash, in one PK lookup.
func (s *Service) principalAndHash(ctx context.Context, id int64) (*Principal, string, error) {
	u, err := s.rq.GetPrincipalAndHash(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (u.Disabled || u.Kind != KindLocal)) {
		return nil, "", ErrBadCredentials
	}
	if err != nil {
		return nil, "", err
	}
	return &Principal{UserID: id, Username: u.Username, IsAdmin: u.IsAdmin, Kind: u.Kind}, u.PasswordHash, nil
}

func (s *Service) principalByID(ctx context.Context, id int64) (*Principal, error) {
	u, err := s.rq.GetPrincipal(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && u.Disabled) {
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	return &Principal{UserID: id, Username: u.Username, IsAdmin: u.IsAdmin, Kind: u.Kind}, nil
}

// UIHeader is the header the web UI's API client sends (value "1").
const UIHeader = "X-Telesphorepo-UI"

// wantsChallenge reports whether a 401 to r carries the Basic challenge. The JSON API and
// requests from the web UI never get it, so a browser never opens its password dialog; format
// routes keep it for package clients.
func wantsChallenge(r *http.Request) bool {
	return r.Header.Get(UIHeader) != "1" && r.URL.Path != "/api" && !strings.HasPrefix(r.URL.Path, "/api/")
}

func unauthorized(w http.ResponseWriter, r *http.Request, msg string) {
	if wantsChallenge(r) {
		w.Header().Set("WWW-Authenticate", challenge)
	}
	httpx.Error(w, http.StatusUnauthorized, msg)
}

// writeAuthError maps an authenticate error to a response.
func writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	var rl *rateLimitedError
	switch {
	case errors.As(err, &rl):
		setRetryAfter(w, rl.wait)
		httpx.Error(w, http.StatusTooManyRequests, ErrRateLimited.Error())
	case errors.Is(err, ErrBadCredentials):
		unauthorized(w, r, ErrBadCredentials.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, "internal error")
	}
}

// ErrSessionRequired is the answer to a request that needs a UI session but came with HTTP
// Basic (a password or a read token) or with no credential.
var ErrSessionRequired = errors.New("sign in through the web UI for this action")

// SessionRequired answers 401 {"error": ErrSessionRequired}. It sends no Basic challenge, so a
// browser does not open its password dialog.
func SessionRequired(w http.ResponseWriter) {
	httpx.Error(w, http.StatusUnauthorized, ErrSessionRequired.Error())
}

// Identify authenticates the request: HTTP Basic (password, read token or publish-session
// token) everywhere, "Authorization: Bearer trs_..." (a publish-session token), and the session
// cookie when allowSession (the JSON API). It writes the error response and returns ok=false
// when the request must stop. On the JSON API:
//   - a password or a read token reaches only GET and HEAD routes (401 ErrSessionRequired
//     elsewhere). POST /api/repos/{name}/upload and DELETE /api/repos/{name}/files are left to
//     their gates, which take only a publish session or an admin UI session. The API refuses
//     admin routes to these credentials too (see api.admin), so every change of state and every
//     admin route needs a UI session, and an admin UI session exists only after the second
//     factor (outside dev mode);
//   - a publish-session token reaches only /api/publish/, POST /api/repos/{name}/upload and
//     DELETE /api/repos/{name}/files (403 elsewhere);
//   - a UI session of a local admin without a second factor reaches only /api/me,
//     /api/auth/logout and /api/security (403 {"error":"second_factor_required"} elsewhere),
//     except in dev mode.
func (s *Service) Identify(w http.ResponseWriter, r *http.Request, allowSession bool) (*Principal, bool) {
	return s.identify(w, r, allowSession, allowSession)
}

// identify is Identify with the JSON API rules (api) and the session cookie (cookie) chosen
// apart. A format route reads with the cookie (cookie) but keeps the format-route rules for the
// other credentials (api false).
func (s *Service) identify(w http.ResponseWriter, r *http.Request, api, cookie bool) (*Principal, bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		user, pass, ok := r.BasicAuth()
		if !ok && len(h) > 7 && strings.EqualFold(h[:7], "bearer ") && strings.HasPrefix(strings.TrimSpace(h[7:]), publishTokenPrefix) {
			user, pass, ok = "", strings.TrimSpace(h[7:]), true
		}
		if !ok {
			if len(h) >= 6 && strings.EqualFold(h[:6], "basic ") {
				unauthorized(w, r, ErrBadCredentials.Error())
				return nil, false
			}
		} else {
			p, err := s.authenticate(r.Context(), s.clientIP(r), user, pass, true)
			if errors.Is(err, errPublishEnded) {
				s.publishRequired(w, r)
				return nil, false
			}
			if err != nil {
				writeAuthError(w, r, err)
				return nil, false
			}
			if api && p.Publish != nil && !publishTokenRoute(r) {
				httpx.Error(w, http.StatusForbidden, "a publish session token only publishes")
				return nil, false
			}
			if api && p.Publish == nil && !safeMethod(r.Method) && !repoWriteRoute(r) {
				SessionRequired(w)
				return nil, false
			}
			return p, true
		}
	}
	if cookie {
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			p, err := s.sessionPrincipal(r.Context(), c.Value)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "internal error")
				return nil, false
			}
			if p != nil && !setupRoute(r.URL.Path) {
				setup, err := s.SetupOnly(r.Context(), p)
				if err != nil {
					httpx.Error(w, http.StatusInternalServerError, "internal error")
					return nil, false
				}
				if setup {
					httpx.Error(w, http.StatusForbidden, ErrNoSecondFactor.Error())
					return nil, false
				}
			}
			return p, true
		}
	}
	return nil, true
}

// setupRoute reports whether a setup-only session may reach path.
func setupRoute(path string) bool {
	return path == "/api/me" || path == "/api/auth/logout" ||
		path == "/api/security" || strings.HasPrefix(path, "/api/security/")
}

// safeMethod reports whether method only reads.
func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// publishTokenRoute reports whether a publish-session token may reach r on the JSON API:
// /api/publish/..., POST /api/repos/{name}/upload and DELETE /api/repos/{name}/files.
func publishTokenRoute(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/publish/") || repoWriteRoute(r)
}

// repoWriteRoute reports whether r is POST /api/repos/{name}/upload, POST
// /api/repos/{name}/upload/scan or DELETE /api/repos/{name}/files.
func repoWriteRoute(r *http.Request) bool {
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/repos/")
	if !ok {
		return false
	}
	name, tail, ok := strings.Cut(rest, "/")
	if !ok || name == "" {
		return false
	}
	return (r.Method == http.MethodPost && (tail == "upload" || tail == "upload/scan")) || (r.Method == http.MethodDelete && tail == "files")
}
