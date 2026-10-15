// Package api implements the JSON API under /api/ (spec §10).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/files"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/move"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
	"github.com/Telesphorepo/Telesphorepo/internal/nexus"
	"github.com/Telesphorepo/Telesphorepo/internal/publish"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

type API struct {
	auth     *auth.Service
	repos    *repos.Service
	assets   *assets.Service
	cfg      config.Provider
	settings *settings.Service
	moves    *move.Service
	nexus    *nexus.Service
	publish  *publish.Service
	files    *files.Service
	audit    *audit.Service
	rules    *netrules.Service
	access   *access.Service
	keys     *signing.Keys
	sso      Routes
}

// Routes is a set of extra routes mounted on the API mux (the OIDC handler).
type Routes interface {
	Register(mux *http.ServeMux)
}

// New builds the API. sso (may be nil) mounts GET /api/auth/oidc/{login,callback}. The format
// endpoints read the format registry (internal/format).
func New(a *auth.Service, rs *repos.Service, as *assets.Service, st *settings.Service, mv *move.Service, nx *nexus.Service, ps *publish.Service, fs *files.Service, au *audit.Service, nr *netrules.Service, ax *access.Service, keys *signing.Keys, sso Routes) *API {
	return &API{
		auth: a, repos: rs, assets: as, cfg: st.Config, settings: st, moves: mv, nexus: nx, publish: ps,
		files: fs, audit: au, rules: nr, access: ax, keys: keys, sso: sso,
	}
}

// Handler returns the /api/ handler, already wrapped in http.CrossOriginProtection.
// The server mounts it at "/api/".
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	a.registerSecurity(mux) // sign-in (password, then second factor) and second-factor setup
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.HandleFunc("GET /api/auth/config", a.authConfig)
	mux.HandleFunc("GET /api/me", a.me)
	if a.sso != nil {
		a.sso.Register(mux)
	}

	mux.HandleFunc("GET /api/tokens", a.listTokens)
	mux.HandleFunc("POST /api/tokens", a.createToken)
	mux.HandleFunc("DELETE /api/tokens/{id}", a.deleteToken)

	mux.HandleFunc("GET /api/users", a.listUsers)
	mux.HandleFunc("POST /api/users", a.createUser)
	mux.HandleFunc("GET /api/users/{username}", a.getUser)
	mux.HandleFunc("PATCH /api/users/{username}", a.updateUser)

	mux.HandleFunc("GET /api/formats", a.listFormats)

	mux.HandleFunc("GET /api/repos", a.listRepos)
	mux.HandleFunc("POST /api/repos", a.createRepo)
	mux.HandleFunc("GET /api/repos/{name}", a.getRepo)
	mux.HandleFunc("PATCH /api/repos/{name}", a.updateRepo)
	mux.HandleFunc("DELETE /api/repos/{name}", a.deleteRepo)
	mux.HandleFunc("GET /api/repos/{name}/permissions", a.listPermissions)
	mux.HandleFunc("PUT /api/repos/{name}/permissions/{username}", a.setPermission)
	mux.HandleFunc("DELETE /api/repos/{name}/permissions/{username}", a.deletePermission)
	mux.HandleFunc("GET /api/repos/{name}/assets", a.listAssets)
	mux.HandleFunc("GET /api/repos/{name}/status", a.repoStatus)
	mux.HandleFunc("GET /api/repos/{name}/usage", a.repoUsage)
	mux.HandleFunc("GET /api/repos/{name}/snippets", a.artifactSnippets)
	mux.HandleFunc("GET /api/repos/{name}/tree", a.tree)
	mux.HandleFunc("GET /api/repos/{name}/file", a.fileDetails)
	mux.HandleFunc("GET /api/repos/{name}/versions", a.versions)
	mux.HandleFunc("GET /api/repos/{name}/builds", a.builds)
	mux.HandleFunc("DELETE /api/repos/{name}/files", a.deleteFiles)
	mux.HandleFunc("POST /api/repos/{name}/upload", a.upload)
	mux.HandleFunc("POST /api/repos/{name}/upload/scan", a.scanUpload)

	mux.HandleFunc("GET /api/audit", a.listAudit)

	mux.HandleFunc("GET /api/search", a.search)

	mux.HandleFunc("GET /api/settings", a.getSettings)
	mux.HandleFunc("PATCH /api/settings", a.patchSettings)
	mux.HandleFunc("POST /api/settings/storage/test", a.testStorage)
	mux.HandleFunc("POST /api/settings/oidc/test", a.testOIDC)

	a.registerAccess(mux)
	a.registerNetwork(mux)
	a.registerSigningKeys(mux)
	a.registerMoves(mux)
	a.registerNexus(mux)
	a.registerPublish(mux)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusNotFound, "not found")
	})
	return http.NewCrossOriginProtection().Handler(mux)
}

// --- helpers ---

// fail maps service errors to status codes without leaking internals.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalid), errors.Is(err, repos.ErrInvalid):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrNotFound), errors.Is(err, repos.ErrNotFound), errors.Is(err, assets.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, auth.ErrExists), errors.Is(err, repos.ErrExists):
		httpx.Error(w, http.StatusConflict, "already exists")
	case errors.Is(err, auth.ErrSSOAccount), errors.Is(err, auth.ErrPasswordLoginDisabled):
		httpx.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, auth.ErrBadCredentials):
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
	case errors.Is(err, auth.ErrRateLimited):
		if w.Header().Get("Retry-After") == "" {
			w.Header().Set("Retry-After", "6")
		}
		httpx.Error(w, http.StatusTooManyRequests, "too many failed attempts")
	case errors.Is(err, context.Canceled):
		httpx.Error(w, http.StatusServiceUnavailable, "request canceled")
	default:
		slog.ErrorContext(r.Context(), "api error", "method", r.Method, "path", r.URL.Path, "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal error")
	}
}

// user identifies the caller and requires authentication (401 JSON when anonymous). A password
// or a read token reaches only GET and HEAD routes (auth.Identify answers 401 otherwise). A
// publish-session token gets 403: it only publishes, and only DELETE /api/publish/sessions/{id}
// (which checks the token itself) takes it on the API.
func (a *API) user(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p, ok := a.auth.Identify(w, r, true)
	if !ok {
		return nil, false
	}
	if p == nil {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			httpx.Error(w, http.StatusUnauthorized, "authentication required")
		} else {
			auth.SessionRequired(w)
		}
		return nil, false
	}
	if p.Publish != nil {
		httpx.Error(w, http.StatusForbidden, "a publish session token only publishes")
		return nil, false
	}
	return p, true
}

// admin requires the UI session of an admin: 401 without a UI session (anonymous, a password or
// a read token), 403 for a session of a user who is not an admin. Outside dev mode an admin
// session exists only after the second factor, so a password alone never reaches an admin route.
func (a *API) admin(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p, ok := a.auth.Identify(w, r, true)
	if !ok {
		return nil, false
	}
	if p == nil || !p.Session {
		auth.SessionRequired(w)
		return nil, false
	}
	if !p.IsAdmin {
		httpx.Error(w, http.StatusForbidden, "admin required")
		return nil, false
	}
	return p, true
}

// adminRepo requires an admin and loads repo {name} (404 if missing).
func (a *API) adminRepo(w http.ResponseWriter, r *http.Request) (*repos.Repo, bool) {
	_, repo, ok := a.adminRepoP(w, r)
	return repo, ok
}

// adminRepoP is adminRepo that also returns the admin.
func (a *API) adminRepoP(w http.ResponseWriter, r *http.Request) (*auth.Principal, *repos.Repo, bool) {
	p, ok := a.admin(w, r)
	if !ok {
		return nil, nil, false
	}
	repo, err := a.repos.ByName(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return nil, nil, false
	}
	return p, repo, true
}

// baseURL is the base_url setting, else the URL the request came in on.
func (a *API) baseURL(r *http.Request) string {
	if base := a.cfg().BaseURL; base != "" {
		return base
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

type list[T any] struct {
	Items []T     `json:"items"`
	Next  *string `json:"next"`
}

func cursor(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// --- auth ---

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.auth.Logout(w, r); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// me returns the caller. second_factor_required is true for a UI session of a local admin
// without a TOTP factor or a passkey (outside dev mode): that session reaches only the setup
// routes.
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	p, ok := a.user(w, r)
	if !ok {
		return
	}
	u, err := a.auth.GetUser(r.Context(), p.Username)
	if err != nil {
		fail(w, r, err)
		return
	}
	setupOnly, err := a.auth.SetupOnly(r.Context(), p)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, loginUser{u, setupOnly})
}

// --- tokens ---

func (a *API) listTokens(w http.ResponseWriter, r *http.Request) {
	p, ok := a.user(w, r)
	if !ok {
		return
	}
	ts, err := a.auth.ListTokens(r.Context(), p.UserID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if ts == nil {
		ts = []*auth.Token{}
	}
	httpx.WriteJSON(w, http.StatusOK, ts)
}

func (a *API) createToken(w http.ResponseWriter, r *http.Request) {
	p, ok := a.user(w, r)
	if !ok {
		return
	}
	// SSO accounts are UI-only (spec §7.5).
	if p.Kind != auth.KindLocal {
		httpx.Error(w, http.StatusForbidden, "SSO accounts cannot create API tokens")
		return
	}
	var in struct {
		Name          string `json:"name"`
		ExpiresInDays *int   `json:"expires_in_days"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	var exp time.Duration
	if in.ExpiresInDays != nil {
		if d := *in.ExpiresInDays; d < 1 || d > 36500 {
			httpx.Error(w, http.StatusBadRequest, "expires_in_days must be between 1 and 36500")
			return
		}
		exp = time.Duration(*in.ExpiresInDays) * 24 * time.Hour
	}
	t, plain, err := a.auth.CreateToken(r.Context(), p.UserID, in.Name, exp)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, struct {
		ID         int64      `json:"id"`
		Name       string     `json:"name"`
		CreatedAt  time.Time  `json:"created_at"`
		ExpiresAt  *time.Time `json:"expires_at"`
		LastUsedAt *time.Time `json:"last_used_at"`
		Token      string     `json:"token"`
	}{t.ID, t.Name, t.CreatedAt, t.ExpiresAt, t.LastUsedAt, plain})
}

func (a *API) deleteToken(w http.ResponseWriter, r *http.Request) {
	p, ok := a.user(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid token id")
		return
	}
	if err := a.auth.DeleteToken(r.Context(), p, id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- users ---

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	us, next, err := a.auth.ListUsers(r.Context(), r.URL.Query().Get("after"), httpx.Limit(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	if us == nil {
		us = []*auth.User{}
	}
	httpx.WriteJSON(w, http.StatusOK, list[*auth.User]{us, cursor(next)})
}

// getUser returns one account by name (admin): the person page of the UI.
func (a *API) getUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	u, err := a.auth.GetUser(r.Context(), r.PathValue("username"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
	if !ok {
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	u, err := a.auth.CreateUser(r.Context(), in.Username, in.Password, in.IsAdmin)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.record(r, p, audit.UserCreate, nil, u.Username, map[string]any{"is_admin": u.IsAdmin})
	httpx.WriteJSON(w, http.StatusCreated, u)
}

func (a *API) updateUser(w http.ResponseWriter, r *http.Request) {
	p, ok := a.user(w, r)
	if !ok {
		return
	}
	var in struct {
		Password *string `json:"password"`
		IsAdmin  *bool   `json:"is_admin"`
		Disabled *bool   `json:"disabled"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	name := r.PathValue("username")
	if !p.IsAdmin && (name != p.Username || in.IsAdmin != nil || in.Disabled != nil) {
		httpx.Error(w, http.StatusForbidden, "admin required")
		return
	}
	u, err := a.auth.UpdateUser(r.Context(), name, in.Password, in.IsAdmin, in.Disabled)
	if err != nil {
		fail(w, r, err)
		return
	}
	detail := map[string]any{}
	if in.Password != nil {
		detail["password"] = "changed" // never the value
	}
	if in.IsAdmin != nil {
		detail["is_admin"] = *in.IsAdmin
	}
	if in.Disabled != nil {
		detail["disabled"] = *in.Disabled
	}
	if len(detail) > 0 {
		a.record(r, p, audit.UserUpdate, nil, u.Username, detail)
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

// --- repos ---

type repoJSON struct {
	Name          string          `json:"name"`
	Format        string          `json:"format"`
	Description   string          `json:"description"`
	AnonymousRead bool            `json:"anonymous_read"`
	Config        json.RawMessage `json:"config"`
	Permission    string          `json:"permission"`
	LockedBy      string          `json:"locked_by"` // why uploads answer 409; "" when they do not
	// The repo counters: every asset row counts. LastPublishedAt is null before the first publish.
	FileCount       int64      `json:"file_count"`
	TotalSize       int64      `json:"total_size"`
	LastPublishedAt *time.Time `json:"last_published_at"`
}

func toRepoJSON(r *repos.Repo, perm string) repoJSON {
	cfg := r.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	out := repoJSON{
		Name: r.Name, Format: r.Format, Description: r.Description, AnonymousRead: r.AnonymousRead, Config: cfg,
		Permission: perm, LockedBy: r.LockedBy, FileCount: r.FileCount, TotalSize: r.TotalSize,
	}
	if !r.LastPublishedAt.IsZero() {
		t := r.LastPublishedAt.UTC()
		out.LastPublishedAt = &t
	}
	return out
}

// record writes an audit event of the caller p for repo (nil: none).
func (a *API) record(r *http.Request, p *auth.Principal, action string, repo *repos.Repo, target string, detail map[string]any) {
	e := audit.Event{Actor: auth.ActorOf(p), Action: action, Target: target, Detail: detail}
	if repo != nil {
		e.RepoID, e.Repo = repo.ID, repo.Name
	}
	a.audit.Record(r.Context(), r, e)
}

func (a *API) permission(ctx context.Context, p *auth.Principal, repo *repos.Repo) (string, error) {
	if p != nil && p.IsAdmin {
		return "admin", nil
	}
	l, err := a.auth.Access(ctx, p, repo)
	if err != nil {
		return "", err
	}
	return l.String(), nil
}

func (a *API) listRepos(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth.Identify(w, r, true)
	if !ok {
		return
	}
	ctx := r.Context()
	all, err := a.repos.List(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}
	ids, isAll, err := a.auth.ReadableRepoIDs(ctx, p)
	if err != nil {
		fail(w, r, err)
		return
	}
	readable := make(map[int64]bool, len(ids))
	for _, id := range ids {
		readable[id] = true
	}
	out := []repoJSON{}
	for _, repo := range all {
		if !isAll && !readable[repo.ID] && !repo.AnonymousRead {
			continue
		}
		perm, err := a.permission(ctx, p, repo)
		if err != nil {
			fail(w, r, err)
			return
		}
		out = append(out, toRepoJSON(repo, perm))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (a *API) createRepo(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
	if !ok {
		return
	}
	var in struct {
		Name          string          `json:"name"`
		Format        string          `json:"format"`
		Description   string          `json:"description"`
		AnonymousRead bool            `json:"anonymous_read"`
		Config        json.RawMessage `json:"config"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	repo, err := a.repos.Create(r.Context(), in.Name, in.Format, in.AnonymousRead, in.Description, in.Config)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.record(r, p, audit.RepoCreate, repo, repo.Name, map[string]any{"format": repo.Format, "anonymous_read": repo.AnonymousRead})
	httpx.WriteJSON(w, http.StatusCreated, toRepoJSON(repo, "admin"))
}

func (a *API) getRepo(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := a.auth.RepoMeta(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	perm, err := a.permission(r.Context(), p, repo)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toRepoJSON(repo, perm))
}

func (a *API) updateRepo(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
	if !ok {
		return
	}
	var in struct {
		AnonymousRead *bool           `json:"anonymous_read"`
		Description   *string         `json:"description"`
		Config        json.RawMessage `json:"config"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	old, repo, err := a.repos.Update(r.Context(), r.PathValue("name"), in.AnonymousRead, in.Description, in.Config)
	if err != nil {
		fail(w, r, err)
		return
	}
	var changed []string
	if old.AnonymousRead != repo.AnonymousRead {
		changed = append(changed, "anonymous_read")
	}
	if old.Description != repo.Description {
		changed = append(changed, "description")
	}
	if string(old.Config) != string(repo.Config) {
		changed = append(changed, "config")
	}
	if len(changed) > 0 {
		a.record(r, p, audit.RepoUpdate, repo, repo.Name, map[string]any{"fields": changed})
	}
	if o, ok := format.Get(repo.Format).(format.ConfigObserver); ok && in.Config != nil {
		if err := o.RepoConfigChanged(r.Context(), old, repo); err != nil {
			fail(w, r, err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, toRepoJSON(repo, "admin"))
}

// deleteRepo deletes a repo with all its files and blobs (admin): 200 {deleted: <file count>}.
func (a *API) deleteRepo(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
	if !ok {
		return
	}
	repo, err := a.repos.ByName(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	n, err := a.files.DeleteRepo(r.Context(), repo)
	if err == nil {
		a.rules.Forget(repo.ID) // the rows cascaded with the repo; an id can be used again
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	// The repo row is gone, so the event keeps only its name (in the detail).
	a.audit.Record(r.Context(), r, audit.Event{Actor: auth.ActorOf(p), Action: audit.RepoDelete,
		Repo: repo.Name, Target: repo.Name, Detail: map[string]any{"count": n}})
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"deleted": n})
}

// --- permissions ---

func (a *API) listPermissions(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.adminRepo(w, r)
	if !ok {
		return
	}
	ps, err := a.auth.ListPermissions(r.Context(), repo.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if ps == nil {
		ps = []auth.Permission{}
	}
	httpx.WriteJSON(w, http.StatusOK, ps)
}

func (a *API) setPermission(w http.ResponseWriter, r *http.Request) {
	p, repo, ok := a.adminRepoP(w, r)
	if !ok {
		return
	}
	var in struct {
		Level string `json:"level"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	var lvl auth.Level
	switch in.Level {
	case "read":
		lvl = auth.Read
	case "write":
		lvl = auth.Write
	default:
		httpx.Error(w, http.StatusBadRequest, `level must be "read" or "write"`)
		return
	}
	if err := a.auth.SetPermission(r.Context(), repo.ID, r.PathValue("username"), lvl); err != nil {
		fail(w, r, err)
		return
	}
	a.record(r, p, audit.PermissionSet, repo, r.PathValue("username"), map[string]any{"level": in.Level})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deletePermission(w http.ResponseWriter, r *http.Request) {
	p, repo, ok := a.adminRepoP(w, r)
	if !ok {
		return
	}
	if err := a.auth.DeletePermission(r.Context(), repo.ID, r.PathValue("username")); err != nil {
		fail(w, r, err)
		return
	}
	a.record(r, p, audit.PermissionDelete, repo, r.PathValue("username"), nil)
	w.WriteHeader(http.StatusNoContent)
}

// --- assets & search ---

type assetJSON struct {
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	ContentType string    `json:"content_type"`
	Kind        string    `json:"kind"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (a *API) listAssets(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.Read, true)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, next, err := a.assets.List(r.Context(), repo.ID, q.Get("prefix"), q.Get("after"), httpx.Limit(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	out := list[assetJSON]{Items: make([]assetJSON, 0, len(items)), Next: cursor(next)}
	for _, x := range items {
		out.Items = append(out.Items, assetJSON{x.Path, x.Size, x.SHA256, x.ContentType, x.Kind, x.UpdatedAt})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

type hitJSON struct {
	Repo      string    `json:"repo"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Kind      string    `json:"kind"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (a *API) search(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth.Identify(w, r, true)
	if !ok {
		return
	}
	ctx, qs := r.Context(), r.URL.Query()
	q := qs.Get("q")
	if utf8.RuneCountInString(q) < 3 {
		httpx.Error(w, http.StatusBadRequest, "q must be at least 3 characters")
		return
	}
	var after int64
	if s := qs.Get("after"); s != "" {
		var err error
		if after, err = strconv.ParseInt(s, 10, 64); err != nil || after < 0 {
			httpx.Error(w, http.StatusBadRequest, "invalid cursor")
			return
		}
	}
	empty := list[hitJSON]{Items: []hitJSON{}}

	ids, isAll, err := a.auth.ReadableRepoIDs(ctx, p)
	if err != nil {
		fail(w, r, err)
		return
	}
	if name := qs.Get("repo"); name != "" {
		repo, err := a.repos.ByName(ctx, name)
		if errors.Is(err, repos.ErrNotFound) {
			httpx.WriteJSON(w, http.StatusOK, empty)
			return
		} else if err != nil {
			fail(w, r, err)
			return
		}
		if !isAll && !slices.Contains(ids, repo.ID) {
			httpx.WriteJSON(w, http.StatusOK, empty)
			return
		}
		ids, isAll = []int64{repo.ID}, false
	}
	// Repos whose read rules refuse the caller's address drop out of the results.
	if denied := a.rules.DeniedRepos(httpx.ClientIP(r)); len(denied) > 0 {
		if isAll {
			all, err := a.repos.List(ctx)
			if err != nil {
				fail(w, r, err)
				return
			}
			ids, isAll = make([]int64, 0, len(all)), false
			for _, repo := range all {
				ids = append(ids, repo.ID)
			}
		}
		ids = slices.DeleteFunc(ids, func(id int64) bool { return denied[id] })
	}
	if isAll {
		ids = nil
	} else if len(ids) == 0 {
		httpx.WriteJSON(w, http.StatusOK, empty)
		return
	}
	hits, next, err := a.assets.Search(ctx, q, ids, after, httpx.Limit(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	out := list[hitJSON]{Items: make([]hitJSON, 0, len(hits))}
	for _, h := range hits {
		x := h.Asset
		out.Items = append(out.Items, hitJSON{h.RepoName, x.Path, x.Size, x.SHA256, x.Kind, x.UpdatedAt})
	}
	if next > 0 {
		out.Next = cursor(strconv.FormatInt(next, 10))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
