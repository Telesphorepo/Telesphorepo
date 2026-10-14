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

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// DebianStatus is satisfied by *debian.Handler.
type DebianStatus interface {
	Pending(ctx context.Context, repoID int64) (int, error)
	ConfigChanged(ctx context.Context, repo *repos.Repo) error
}

type API struct {
	auth     *auth.Service
	repos    *repos.Service
	assets   *assets.Service
	debian   DebianStatus
	cfg      config.Provider
	settings *settings.Service
}

func New(a *auth.Service, rs *repos.Service, as *assets.Service, deb DebianStatus, st *settings.Service) *API {
	return &API{auth: a, repos: rs, assets: as, debian: deb, cfg: st.Config, settings: st}
}

// Handler returns the /api/ handler, already wrapped in http.CrossOriginProtection.
// The server mounts it at "/api/".
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.HandleFunc("GET /api/auth/config", a.authConfig)
	mux.HandleFunc("GET /api/me", a.me)

	mux.HandleFunc("GET /api/tokens", a.listTokens)
	mux.HandleFunc("POST /api/tokens", a.createToken)
	mux.HandleFunc("DELETE /api/tokens/{id}", a.deleteToken)

	mux.HandleFunc("GET /api/users", a.listUsers)
	mux.HandleFunc("POST /api/users", a.createUser)
	mux.HandleFunc("PATCH /api/users/{username}", a.updateUser)

	mux.HandleFunc("GET /api/repos", a.listRepos)
	mux.HandleFunc("POST /api/repos", a.createRepo)
	mux.HandleFunc("GET /api/repos/{name}", a.getRepo)
	mux.HandleFunc("PATCH /api/repos/{name}", a.updateRepo)
	mux.HandleFunc("GET /api/repos/{name}/permissions", a.listPermissions)
	mux.HandleFunc("PUT /api/repos/{name}/permissions/{username}", a.setPermission)
	mux.HandleFunc("DELETE /api/repos/{name}/permissions/{username}", a.deletePermission)
	mux.HandleFunc("GET /api/repos/{name}/assets", a.listAssets)
	mux.HandleFunc("GET /api/repos/{name}/debian/status", a.debianStatus)

	mux.HandleFunc("GET /api/search", a.search)

	mux.HandleFunc("GET /api/settings", a.getSettings)
	mux.HandleFunc("PATCH /api/settings", a.patchSettings)
	mux.HandleFunc("POST /api/settings/storage/test", a.testStorage)
	mux.HandleFunc("GET /api/settings/signing-key", a.getSigningKey)
	mux.HandleFunc("POST /api/settings/signing-key", a.setSigningKey)

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

// user identifies the caller and requires authentication (401 JSON when anonymous).
func (a *API) user(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p, ok := a.auth.Identify(w, r, true)
	if !ok {
		return nil, false
	}
	if p == nil {
		httpx.Error(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return p, true
}

// admin requires an authenticated admin (401 anonymous, 403 otherwise).
func (a *API) admin(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p, ok := a.user(w, r)
	if !ok {
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
	if _, ok := a.admin(w, r); !ok {
		return nil, false
	}
	repo, err := a.repos.ByName(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return nil, false
	}
	return repo, true
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

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	u, err := a.auth.Login(w, r, in.Username, in.Password)
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) || errors.Is(err, auth.ErrInvalid) {
			err = auth.ErrBadCredentials
		}
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.auth.Logout(w, r); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

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
	httpx.WriteJSON(w, http.StatusOK, u)
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
	// A token must not mint further tokens: that would escape its expiry (spec §7.1).
	if p.TokenID != 0 {
		httpx.Error(w, http.StatusForbidden, "cannot create tokens with an API token")
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

func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
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
	// A token holder must not be able to take over the account by setting its password.
	if in.Password != nil && p.TokenID != 0 {
		httpx.Error(w, http.StatusForbidden, "cannot change passwords with an API token")
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
	httpx.WriteJSON(w, http.StatusOK, u)
}

// --- repos ---

type repoJSON struct {
	Name          string          `json:"name"`
	Format        string          `json:"format"`
	AnonymousRead bool            `json:"anonymous_read"`
	Config        json.RawMessage `json:"config"`
	Permission    string          `json:"permission"`
}

func toRepoJSON(r *repos.Repo, perm string) repoJSON {
	cfg := r.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	return repoJSON{r.Name, r.Format, r.AnonymousRead, cfg, perm}
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
	if _, ok := a.admin(w, r); !ok {
		return
	}
	var in struct {
		Name          string          `json:"name"`
		Format        string          `json:"format"`
		AnonymousRead bool            `json:"anonymous_read"`
		Config        json.RawMessage `json:"config"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	repo, err := a.repos.Create(r.Context(), in.Name, in.Format, in.AnonymousRead, in.Config)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toRepoJSON(repo, "admin"))
}

func (a *API) getRepo(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.Read, true)
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
	if _, ok := a.admin(w, r); !ok {
		return
	}
	var in struct {
		AnonymousRead *bool           `json:"anonymous_read"`
		Config        json.RawMessage `json:"config"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	repo, err := a.repos.Update(r.Context(), r.PathValue("name"), in.AnonymousRead, in.Config)
	if err != nil {
		fail(w, r, err)
		return
	}
	if in.Config != nil {
		if err := a.debian.ConfigChanged(r.Context(), repo); err != nil {
			fail(w, r, err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, toRepoJSON(repo, "admin"))
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
	repo, ok := a.adminRepo(w, r)
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
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deletePermission(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.adminRepo(w, r)
	if !ok {
		return
	}
	if err := a.auth.DeletePermission(r.Context(), repo.ID, r.PathValue("username")); err != nil {
		fail(w, r, err)
		return
	}
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

// --- debian ---

func (a *API) debianStatus(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := a.auth.RepoGate(w, r, r.PathValue("name"), repos.FormatDebian, auth.Read, true)
	if !ok {
		return
	}
	n, err := a.debian.Pending(r.Context(), repo.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"pending": n})
}
