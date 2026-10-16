// Package oidc is the sso module: sign-in with an OIDC identity provider through the
// authorization-code flow with PKCE (S256), state and nonce, against the issuer of the "sso"
// settings group. Any person at the provider can sign in; a first sign-in creates an SSO account.
// When the permission claim is set, each sign-in sets the server permissions of the account
// from the claim's values (oidc.permission_map); else a sign-in never changes them. The same
// flow, with a forced new sign-in at the provider, lets an SSO user approve a device.
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
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/publish"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

const (
	stateCookie      = "tr_oidc"
	stateCookiePath  = "/api/auth/oidc"
	stateTTL         = 10 * time.Minute
	maxStates        = 10000
	discoveryTimeout = 10 * time.Second
	// maxAuthAge is the oldest sign-in (ID token auth_time) that approves a device.
	maxAuthAge = 5 * time.Minute
)

// Error codes of the sign-in page (/#/login?error=...) and the approval page
// (/#/device?code=...&error=...).
const (
	errUsernameConflict = "username_conflict"
	errInvalidUsername  = "invalid_username"
	errAccountDisabled  = "account_disabled"
	errSSOFailed        = "sso_failed"
	// Approval page only.
	errNotSSO       = "not_sso"       // the UI session is not an SSO account
	errWrongAccount = "wrong_account" // the provider signed in another account
	errNoAuthTime   = "no_auth_time"  // the ID token has no auth_time
	errStaleSignIn  = "stale_sign_in" // auth_time is older than maxAuthAge: the provider ignored prompt=login
)

// Handler serves GET /api/auth/oidc/login, GET /api/auth/oidc/device and
// GET /api/auth/oidc/callback.
type Handler struct {
	auth     *auth.Service
	publish  *publish.Service
	log      *slog.Logger
	settings *settings.Service
	audit    *audit.Log

	provMu sync.Mutex
	prov   *providerEntry // built lazily, keyed by issuer + client id

	states *stateStore
}

type providerEntry struct {
	key string
	p   *gooidc.Provider
}

// cfg returns the current SSO configuration.
func (h *Handler) cfg() *Config {
	c := ConfigView.Get(h.settings.Current())
	return &c
}

// PermManage edits the sign-in options (the settings group "sso").
const PermManage perm.ID = "signin.manage"

// The audit actions of the sso module.
const (
	ActionSignIn       = "sso.signin"
	ActionSignInFailed = "sso.signin.failed"
)

// Module returns the sso module: the SSO sign-in, the SSO device approval, and the settings
// group "sso" (a change must pass the discovery check of the issuer).
func Module() module.Module {
	h := &Handler{states: &stateStore{m: map[[32]byte]pending{}}}
	return module.Module{
		ID: "sso", Title: "Single sign-on", Requires: []string{"publish"},
		Register: func(x *module.Extender) {
			x.Permissions(perm.Subsystem{ID: "signin", Scope: perm.ScopeServer, Label: "Sign-in options",
				Help: "How people sign in: single sign-on with an identity provider, and password sign-in."},
				perm.Def{ID: PermManage, Label: "Manage sign-in options", Help: "Connect the identity provider, map its groups to permissions, and choose who may sign in with a password.", SecondFactor: true})
			x.AuditActions(
				audit.ActionDef{ID: ActionSignIn, Label: "Signed in with single sign-on", Subsystem: "signin"},
				audit.ActionDef{ID: ActionSignInFailed, Label: "Single sign-on refused", Subsystem: "signin"},
			)
			x.Settings(h.group())
			x.Init(func(_ context.Context, host *module.Host) error {
				h.auth, h.publish, h.settings, h.audit, h.log = host.Auth, module.Use[*publish.Service](host), host.Settings, host.Audit, host.Log
				host.Auth.SetSSO(func() bool { return h.cfg().OIDCEnabled() })
				host.Auth.SetManaged(func() bool { return h.cfg().Managed() })
				return nil
			})
			x.Routes(func(r *route.Router) {
				r.Handle("GET /api/auth/oidc/login", route.Public, h.login)
				r.Handle("GET /api/auth/oidc/device", route.Policy{Accept: auth.CredSession, Anonymous: true}, h.device)
				r.Handle("GET /api/auth/oidc/callback", route.Public, h.callback)
			})
		},
	}
}

func providerKey(c *Config) string { return c.OIDCIssuer + "\x00" + c.OIDCClientID }

// provider returns the discovered provider for the current settings, fetching the discovery
// document when the issuer or client changed. Failures are not cached.
func (h *Handler) provider(ctx context.Context, c *Config) (*gooidc.Provider, error) {
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

func oauthConfig(c *Config, p *gooidc.Provider) *oauth2.Config {
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

func (h *Handler) login(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	w.Header().Set("Cache-Control", "no-store")
	c := h.cfg()
	if !c.OIDCEnabled() {
		httpx.Error(w, r, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	p, err := h.provider(r.Context(), c)
	if err != nil {
		h.log.WarnContext(r.Context(), "OIDC discovery failed", "issuer", c.OIDCIssuer, "error", err.Error())
		httpx.Error(w, r, http.StatusServiceUnavailable, "identity provider unavailable")
		return
	}
	h.start(w, r, c, p, pending{})
}

// device starts a new sign-in at the provider that approves the device of ?code= for the SSO
// account of the UI session. The request asks the provider to sign the user in again
// (prompt=login, max_age=0). Every failure goes back to the approval page with an error code.
func (h *Handler) device(w http.ResponseWriter, r *http.Request, call *route.Call) {
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	c := h.cfg()
	if !c.OIDCEnabled() {
		httpx.Error(w, r, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	code := r.URL.Query().Get("code")
	p := call.P
	if p == nil {
		toDevice(w, r, code, "") // the approval page sends the visitor to sign in
		return
	}
	if p.Kind != auth.KindOIDC {
		toDevice(w, r, code, errNotSSO)
		return
	}
	e, err := h.publish.PendingEnrollment(ctx, code)
	switch {
	case errors.Is(err, publish.ErrNotFound), errors.Is(err, publish.ErrConflict):
		toDevice(w, r, code, "") // the approval page looks the code up and says why it cannot be approved
		return
	case err != nil:
		h.log.ErrorContext(ctx, "SSO device approval: look up the code", "error", err.Error())
		httpx.Error(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	prov, err := h.provider(ctx, c)
	if err != nil {
		h.log.WarnContext(ctx, "OIDC discovery failed", "issuer", c.OIDCIssuer, "error", err.Error())
		toDevice(w, r, e.UserCode, errSSOFailed)
		return
	}
	h.start(w, r, c, prov, pending{userID: p.UserID, userCode: e.UserCode},
		oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
}

// start stores the pending sign-in pend under a new state, sets the state cookie and sends the
// browser to the provider. opts are extra parameters of the authorization request.
func (h *Handler) start(w http.ResponseWriter, r *http.Request, c *Config, p *gooidc.Provider, pend pending, opts ...oauth2.AuthCodeOption) {
	state := randomString()
	pend.nonce, pend.verifier = randomString(), oauth2.GenerateVerifier()
	pend.provKey, pend.expires = providerKey(c), time.Now().Add(stateTTL)
	h.states.put(state, pend)
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state, Path: stateCookiePath, MaxAge: int(stateTTL / time.Second),
		HttpOnly: true, Secure: c.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
	opts = append([]oauth2.AuthCodeOption{oauth2.S256ChallengeOption(pend.verifier), gooidc.Nonce(pend.nonce)}, opts...)
	http.Redirect(w, r, oauthConfig(c, p).AuthCodeURL(state, opts...), http.StatusFound)
}

func (h *Handler) clearCookie(w http.ResponseWriter, c *Config) {
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: "", Path: stateCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: c.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func toLogin(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/#/login?error="+code, http.StatusFound)
}

// toDevice sends the browser back to the approval page of userCode, with an error code when
// errCode is set.
func toDevice(w http.ResponseWriter, r *http.Request, userCode, errCode string) {
	q := url.Values{"code": {userCode}}
	if errCode != "" {
		q.Set("error", errCode)
	}
	http.Redirect(w, r, "/#/device?"+q.Encode(), http.StatusFound)
}

// signedIn is a verified answer of the provider.
type signedIn struct {
	idt    *gooidc.IDToken
	claims map[string]any // of the ID token
	cl     *claimSource
}

func (h *Handler) callback(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	c := h.cfg()
	if !c.OIDCEnabled() {
		httpx.Error(w, r, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	h.clearCookie(w, c)

	// State: must equal the tr_oidc cookie and be pending (single use).
	q := r.URL.Query()
	state := q.Get("state")
	ck, err := r.Cookie(stateCookie)
	if state == "" || err != nil || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(state)) != 1 {
		httpx.Error(w, r, http.StatusBadRequest, "invalid sign-in state; start again")
		return
	}
	pend, ok := h.states.take(state)
	if !ok {
		httpx.Error(w, r, http.StatusBadRequest, "invalid or expired sign-in state; start again")
		return
	}
	if pend.provKey != providerKey(c) {
		httpx.Error(w, r, http.StatusBadRequest, "single sign-on settings changed; start again")
		return
	}
	fail := func(code string) {
		if pend.userCode != "" {
			toDevice(w, r, pend.userCode, code)
		} else {
			toLogin(w, r, code)
		}
	}
	if e := q.Get("error"); e != "" {
		h.log.InfoContext(ctx, "OIDC provider returned an error", "error", e)
		fail(errSSOFailed)
		return
	}
	code := q.Get("code")
	if code == "" {
		httpx.Error(w, r, http.StatusBadRequest, "missing authorization code")
		return
	}

	p, err := h.provider(ctx, c)
	if err != nil {
		h.log.WarnContext(ctx, "OIDC discovery failed", "issuer", c.OIDCIssuer, "error", err.Error())
		httpx.Error(w, r, http.StatusServiceUnavailable, "identity provider unavailable")
		return
	}
	si, ok := h.exchange(ctx, c, p, pend, code)
	if !ok {
		fail(errSSOFailed)
		return
	}

	if pend.userCode != "" {
		h.approval(w, r, pend, si)
		return
	}

	username := NormalizeUsername(claimString(si.cl.get("preferred_username")))
	email := ""
	if v := si.cl.get("email"); v != nil {
		email = strings.ToLower(strings.TrimSpace(claimString(v)))
	}
	var managed *perm.Set
	if c.Managed() {
		set := h.permsOf(ctx, c, si.cl)
		managed = &set
	}
	ctx = audit.WithActor(ctx, audit.Actor{Kind: audit.ActorSSO, Name: username, IP: httpx.ClientIP(r)})
	u, err := h.auth.UpsertOIDCUser(ctx, si.idt.Issuer, si.idt.Subject, username, email, managed)
	switch {
	case errors.Is(err, auth.ErrUsernameConflict):
		h.log.WarnContext(ctx, "SSO login refused: username taken by another account", "username", username)
		h.recordFailed(r, username, "username belongs to another account")
		toLogin(w, r, errUsernameConflict)
		return
	case errors.Is(err, auth.ErrInvalid):
		h.recordFailed(r, username, "invalid username")
		toLogin(w, r, errInvalidUsername)
		return
	case errors.Is(err, auth.ErrDisabled):
		h.recordFailed(r, username, "account disabled")
		toLogin(w, r, errAccountDisabled)
		return
	case err != nil:
		h.log.ErrorContext(ctx, "SSO user upsert", "error", err.Error())
		httpx.Error(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.auth.StartSession(w, r, u.ID, c.OIDCSessionTTL); err != nil {
		h.log.ErrorContext(ctx, "SSO session", "error", err.Error())
		httpx.Error(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	h.log.InfoContext(ctx, "SSO login", "username", u.Username)
	ctx = audit.WithActor(ctx, audit.Actor{Kind: audit.ActorUser, UserID: u.ID, Name: u.Username, IP: httpx.ClientIP(r)})
	_ = h.audit.Record(ctx, audit.Event{Action: ActionSignIn, Target: u.Username})
	http.Redirect(w, r, "/", http.StatusFound)
}

// recordFailed writes sso.signin.failed; username is the name the provider gave.
func (h *Handler) recordFailed(r *http.Request, username, reason string) {
	ctx := audit.WithActor(r.Context(), audit.Actor{Kind: audit.ActorAnonymous, Name: username, IP: httpx.ClientIP(r)})
	_ = h.audit.Record(ctx, audit.Event{Action: ActionSignInFailed, Target: username, Detail: map[string]any{"reason": reason}})
}

// exchange trades the authorization code for tokens and verifies the ID token (signature,
// issuer, audience, expiry, nonce). ok is false after a failure, which it logs.
func (h *Handler) exchange(ctx context.Context, c *Config, p *gooidc.Provider, pend pending, code string) (*signedIn, bool) {
	tok, err := oauthConfig(c, p).Exchange(ctx, code, oauth2.VerifierOption(pend.verifier))
	if err != nil {
		h.log.WarnContext(ctx, "OIDC code exchange failed", "error", err.Error())
		return nil, false
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		h.log.WarnContext(ctx, "OIDC token response has no id_token")
		return nil, false
	}
	idt, err := p.Verifier(&gooidc.Config{ClientID: c.OIDCClientID}).Verify(ctx, rawID)
	if err != nil {
		h.log.WarnContext(ctx, "OIDC ID token verification failed", "error", err.Error())
		return nil, false
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(pend.nonce)) != 1 {
		h.log.WarnContext(ctx, "OIDC ID token nonce mismatch")
		return nil, false
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		h.log.WarnContext(ctx, "OIDC ID token claims unreadable", "error", err.Error())
		return nil, false
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
	return &signedIn{idt: idt, claims: claims, cl: cl}, true
}

// approval ends the sign-in that device started: the provider must have signed in the same
// account within maxAuthAge. It then sends the browser back to the approval page with a
// single-use grant for the code and the account. It starts no session; the UI session stays.
func (h *Handler) approval(w http.ResponseWriter, r *http.Request, pend pending, si *signedIn) {
	ctx := r.Context()
	id, disabled, err := h.auth.OIDCUser(ctx, si.idt.Issuer, si.idt.Subject)
	switch {
	case err != nil:
		h.log.ErrorContext(ctx, "SSO device approval: find the account", "error", err.Error())
		httpx.Error(w, r, http.StatusInternalServerError, "internal error")
		return
	case id != pend.userID:
		h.log.WarnContext(ctx, "SSO device approval refused: the provider signed in another account", "subject", si.idt.Subject)
		toDevice(w, r, pend.userCode, errWrongAccount)
		return
	case disabled:
		toDevice(w, r, pend.userCode, errAccountDisabled)
		return
	}
	at, ok := si.claims["auth_time"].(float64)
	if !ok {
		h.log.WarnContext(ctx, "SSO device approval refused: the ID token has no auth_time")
		toDevice(w, r, pend.userCode, errNoAuthTime)
		return
	}
	if age := time.Since(time.Unix(int64(at), 0)); age > maxAuthAge {
		h.log.WarnContext(ctx, "SSO device approval refused: the provider did not sign the user in again",
			"auth_age", age.Round(time.Second).String())
		toDevice(w, r, pend.userCode, errStaleSignIn)
		return
	}
	grant, err := h.publish.ApprovalGrant(ctx, pend.userID, pend.userCode)
	switch {
	case errors.Is(err, publish.ErrNotFound), errors.Is(err, publish.ErrConflict):
		toDevice(w, r, pend.userCode, "") // the approval page looks the code up and says why it cannot be approved
		return
	case err != nil:
		h.log.ErrorContext(ctx, "SSO device approval: store the grant", "error", err.Error())
		httpx.Error(w, r, http.StatusInternalServerError, "internal error")
		return
	}
	q := url.Values{"code": {pend.userCode}, "grant": {grant}}
	http.Redirect(w, r, "/#/device?"+q.Encode(), http.StatusFound)
}

// --- claims ---

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

// permsOf returns the server permissions that the permission claim gives: the union of the
// permissions that oidc.permission_map lists for each value of the claim (a string or a list).
// Each value's list is parsed as one unit, the unit that the settings check validates. A list
// that does not parse gives nothing and is logged.
func (h *Handler) permsOf(ctx context.Context, c *Config, cl *claimSource) perm.Set {
	var values []string
	switch v := cl.get(c.PermissionClaim).(type) {
	case string:
		values = []string{v}
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				values = append(values, s)
			}
		}
	}
	var out perm.Set
	for _, v := range slices.Compact(slices.Sorted(slices.Values(values))) {
		ids, ok := c.PermissionMap[v]
		if !ok {
			continue
		}
		set, err := perm.Current().Parse(perm.ScopeServer, ids)
		if err != nil {
			h.log.WarnContext(ctx, "OIDC permission map value does not parse", "value", v, "error", err.Error())
			continue
		}
		out = out.Union(set)
	}
	return out
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

// pending is a sign-in that waits for the callback. userCode is set when the sign-in approves
// the device of that code for the SSO account userID; it is empty for a sign-in to the UI.
type pending struct {
	nonce, verifier, provKey string
	expires                  time.Time
	userID                   int64
	userCode                 string
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
