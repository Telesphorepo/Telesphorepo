// Package oidc implements OIDC admin sign-on (design doc §7.5): the authorization-code flow with
// PKCE (S256), state and nonce, against the issuer configured in the "sso" settings group. Only
// administrators sign in this way; a login that does not qualify as admin is refused.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
)

const (
	stateCookie      = "tr_oidc"
	stateCookiePath  = "/api/auth/oidc"
	stateTTL         = 10 * time.Minute
	maxStates        = 10000
	discoveryTimeout = 10 * time.Second
)

// Login-page error codes (/#/login?error=...).
const (
	errNotAuthorized    = "not_authorized"
	errUsernameConflict = "username_conflict"
	errInvalidUsername  = "invalid_username"
	errAccountDisabled  = "account_disabled"
	errSSOFailed        = "sso_failed"
)

// Handler serves GET /api/auth/oidc/login and GET /api/auth/oidc/callback.
type Handler struct {
	auth *auth.Service
	cfg  config.Provider
	log  *slog.Logger

	provMu sync.Mutex
	prov   *providerEntry // built lazily, keyed by issuer + client id

	states *stateStore
}

type providerEntry struct {
	key string
	p   *gooidc.Provider
}

// New returns the SSO handler. Configuration is read live from cfg on every request.
func New(a *auth.Service, cfg config.Provider, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{auth: a, cfg: cfg, log: log, states: &stateStore{m: map[[32]byte]pending{}}}
}

// Register mounts the SSO routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/oidc/login", h.login)
	mux.HandleFunc("GET /api/auth/oidc/callback", h.callback)
}

func providerKey(c *config.Config) string { return c.OIDCIssuer + "\x00" + c.OIDCClientID }

// provider returns the discovered provider for the current settings, fetching the discovery
// document when the issuer or client changed. Failures are not cached.
func (h *Handler) provider(ctx context.Context, c *config.Config) (*gooidc.Provider, error) {
	key := providerKey(c)
	h.provMu.Lock()
	if e := h.prov; e != nil && e.key == key {
		h.provMu.Unlock()
		return e.p, nil
	}
	h.provMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	p, err := gooidc.NewProvider(ctx, c.OIDCIssuer)
	if err != nil {
		return nil, err
	}
	h.provMu.Lock()
	h.prov = &providerEntry{key: key, p: p}
	h.provMu.Unlock()
	return p, nil
}

func oauthConfig(c *config.Config, p *gooidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.OIDCClientID,
		ClientSecret: c.OIDCClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  c.OIDCRedirectURL,
		Scopes:       c.OIDCScopes,
	}
}

func randomString() string {
	b := make([]byte, 32)
	rand.Read(b) // never fails (Go 1.24+)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	c := h.cfg()
	if !c.OIDCEnabled() {
		httpx.Error(w, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	p, err := h.provider(r.Context(), c)
	if err != nil {
		h.log.WarnContext(r.Context(), "OIDC discovery failed", "issuer", c.OIDCIssuer, "error", err.Error())
		httpx.Error(w, http.StatusServiceUnavailable, "identity provider unavailable")
		return
	}
	state, nonce, verifier := randomString(), randomString(), oauth2.GenerateVerifier()
	h.states.put(state, pending{nonce: nonce, verifier: verifier, provKey: providerKey(c), expires: time.Now().Add(stateTTL)})
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state, Path: stateCookiePath, MaxAge: int(stateTTL / time.Second),
		HttpOnly: true, Secure: c.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, oauthConfig(c, p).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), gooidc.Nonce(nonce)), http.StatusFound)
}

func (h *Handler) clearCookie(w http.ResponseWriter, c *config.Config) {
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: "", Path: stateCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: c.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func toLogin(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/#/login?error="+code, http.StatusFound)
}

func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	c := h.cfg()
	if !c.OIDCEnabled() {
		httpx.Error(w, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	h.clearCookie(w, c)

	// State: must equal the tr_oidc cookie and be pending (single use).
	q := r.URL.Query()
	state := q.Get("state")
	ck, err := r.Cookie(stateCookie)
	if state == "" || err != nil || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(state)) != 1 {
		httpx.Error(w, http.StatusBadRequest, "invalid sign-in state; start again")
		return
	}
	pend, ok := h.states.take(state)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "invalid or expired sign-in state; start again")
		return
	}
	if pend.provKey != providerKey(c) {
		httpx.Error(w, http.StatusBadRequest, "single sign-on settings changed; start again")
		return
	}
	if e := q.Get("error"); e != "" {
		h.log.InfoContext(ctx, "OIDC provider returned an error", "error", e)
		toLogin(w, r, errSSOFailed)
		return
	}
	code := q.Get("code")
	if code == "" {
		httpx.Error(w, http.StatusBadRequest, "missing authorization code")
		return
	}

	p, err := h.provider(ctx, c)
	if err != nil {
		h.log.WarnContext(ctx, "OIDC discovery failed", "issuer", c.OIDCIssuer, "error", err.Error())
		httpx.Error(w, http.StatusServiceUnavailable, "identity provider unavailable")
		return
	}
	tok, err := oauthConfig(c, p).Exchange(ctx, code, oauth2.VerifierOption(pend.verifier))
	if err != nil {
		h.log.WarnContext(ctx, "OIDC code exchange failed", "error", err.Error())
		toLogin(w, r, errSSOFailed)
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		h.log.WarnContext(ctx, "OIDC token response has no id_token")
		toLogin(w, r, errSSOFailed)
		return
	}
	idt, err := p.Verifier(&gooidc.Config{ClientID: c.OIDCClientID}).Verify(ctx, rawID)
	if err != nil {
		h.log.WarnContext(ctx, "OIDC ID token verification failed", "error", err.Error())
		toLogin(w, r, errSSOFailed)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(pend.nonce)) != 1 {
		h.log.WarnContext(ctx, "OIDC ID token nonce mismatch")
		toLogin(w, r, errSSOFailed)
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		h.log.WarnContext(ctx, "OIDC ID token claims unreadable", "error", err.Error())
		toLogin(w, r, errSSOFailed)
		return
	}
	cl := &claimSource{id: claims, fetch: func() map[string]any {
		ui, err := p.UserInfo(ctx, oauth2.StaticTokenSource(tok))
		if err != nil {
			h.log.InfoContext(ctx, "OIDC UserInfo request failed", "error", err.Error())
			return nil
		}
		if ui.Subject != idt.Subject {
			h.log.WarnContext(ctx, "OIDC UserInfo subject does not match the ID token; ignoring it")
			return nil
		}
		var m map[string]any
		if err := ui.Claims(&m); err != nil {
			return nil
		}
		return m
	}}

	if !isAdmin(c, cl) {
		if err := h.auth.RevokeOIDCUser(ctx, idt.Issuer, idt.Subject); err != nil {
			h.log.ErrorContext(ctx, "revoke SSO sessions", "error", err.Error())
		}
		h.log.InfoContext(ctx, "SSO login refused: not an admin", "subject", idt.Subject)
		h.auth.RecordSignInFailed(r, NormalizeUsername(claimString(cl.get("preferred_username"))), "sso: not an admin")
		toLogin(w, r, errNotAuthorized)
		return
	}

	username := NormalizeUsername(claimString(cl.get("preferred_username")))
	email := ""
	if v := cl.get("email"); v != nil {
		email = strings.ToLower(strings.TrimSpace(claimString(v)))
	}
	u, err := h.auth.UpsertOIDCUser(ctx, idt.Issuer, idt.Subject, username, email)
	switch {
	case errors.Is(err, auth.ErrUsernameConflict):
		h.log.WarnContext(ctx, "SSO login refused: username taken by another account", "username", username)
		toLogin(w, r, errUsernameConflict)
		return
	case errors.Is(err, auth.ErrInvalid):
		toLogin(w, r, errInvalidUsername)
		return
	case errors.Is(err, auth.ErrDisabled):
		toLogin(w, r, errAccountDisabled)
		return
	case err != nil:
		h.log.ErrorContext(ctx, "SSO user upsert", "error", err.Error())
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.auth.StartSession(w, r, u.ID, c.OIDCSessionTTL); err != nil {
		h.log.ErrorContext(ctx, "SSO session", "error", err.Error())
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.log.InfoContext(ctx, "SSO login", "username", u.Username)
	h.auth.RecordSignIn(r, u.ID, u.Username, "sso")
	http.Redirect(w, r, "/", http.StatusFound)
}

// --- admin rule ---

// claimSource looks claims up in the ID token, falling back to the UserInfo endpoint (fetched
// at most once) for claims the ID token does not carry.
type claimSource struct {
	id      map[string]any
	fetch   func() map[string]any
	fetched bool
	ui      map[string]any
}

func (s *claimSource) get(path string) any {
	if v, ok := lookup(s.id, path); ok {
		return v
	}
	if !s.fetched {
		s.fetched = true
		s.ui = s.fetch()
	}
	v, _ := lookup(s.ui, path)
	return v
}

// lookup resolves a dotted claim path such as "realm_access.roles".
func lookup(m map[string]any, path string) (any, bool) {
	var cur any = m
	for _, seg := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[seg]; !ok {
			return nil, false
		}
	}
	return cur, cur != nil
}

func claimString(v any) string {
	s, _ := v.(string)
	return s
}

// isAdmin applies the admin rule: the admin claim (string or list) contains one of
// oidc.admin_values, or the email is verified and listed in oidc.admin_emails.
func isAdmin(c *config.Config, cl *claimSource) bool {
	if len(c.OIDCAdminValues) > 0 {
		var have []string
		switch v := cl.get(c.OIDCAdminClaim).(type) {
		case string:
			have = []string{v}
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					have = append(have, s)
				}
			}
		}
		for _, want := range c.OIDCAdminValues {
			for _, got := range have {
				if got == want {
					return true
				}
			}
		}
	}
	if len(c.OIDCAdminEmails) > 0 {
		email := strings.ToLower(strings.TrimSpace(claimString(cl.get("email"))))
		verified := false
		switch v := cl.get("email_verified").(type) {
		case bool:
			verified = v
		case string: // some providers send "true"
			verified = strings.EqualFold(v, "true")
		}
		if email != "" && verified {
			for _, want := range c.OIDCAdminEmails {
				if strings.EqualFold(want, email) {
					return true
				}
			}
		}
	}
	return false
}

// NormalizeUsername maps preferred_username to a valid username: lower-cased, characters
// outside [a-z0-9._-] replaced by '-', leading '.', '_' and '-' dropped, at most 64 bytes.
// The result may still be empty (then the login fails with invalid_username).
func NormalizeUsername(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.TrimLeft(b.String(), "._-")
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// --- pending sign-ins ---

type pending struct {
	nonce, verifier, provKey string
	expires                  time.Time
}

// stateStore holds pending sign-ins keyed by SHA-256(state): single use, 10 min TTL, bounded.
type stateStore struct {
	mu sync.Mutex
	m  map[[32]byte]pending
}

func (s *stateStore) put(state string, p pending) {
	k := sha256.Sum256([]byte(state))
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.m) >= maxStates {
		now := time.Now()
		var oldestK [32]byte
		var oldest time.Time
		for key, e := range s.m {
			if now.After(e.expires) {
				delete(s.m, key)
			} else if oldest.IsZero() || e.expires.Before(oldest) {
				oldestK, oldest = key, e.expires
			}
		}
		if len(s.m) >= maxStates {
			delete(s.m, oldestK)
		}
	}
	s.m[k] = p
}

func (s *stateStore) take(state string) (pending, bool) {
	k := sha256.Sum256([]byte(state))
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[k]
	if !ok {
		return pending{}, false
	}
	delete(s.m, k)
	if time.Now().After(p.expires) {
		return pending{}, false
	}
	return p, true
}
