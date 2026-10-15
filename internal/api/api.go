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
	"strings"
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
	mux.HandleFunc("PATCH /api/users", a.setUsersDisabled)
	mux.HandleFunc("GET /api/users/{username}", a.getUser)
	mux.HandleFunc("PATCH /api/users/{username}", a.updateUser)
	mux.HandleFunc("PUT /api/users/{username}/permissions", a.setUserPermissions)
	mux.HandleFunc("GET /api/people", a.listPeople)

	mux.HandleFunc("GET /api/formats", a.listFormats)

	mux.HandleFunc("GET /api/repos", a.listRepos)
	mux.HandleFunc("POST /api/repos", a.createRepo)
	mux.HandleFunc("PATCH /api/repos", a.setReposPublic)
	mux.HandleFunc("POST /api/repos/delete", a.deleteRepos)
	mux.HandleFunc("GET /api/repos/{name}", a.getRepo)
	mux.HandleFunc("PATCH /api/repos/{name}", a.updateRepo)
	mux.HandleFunc("GET /api/repos/{name}/permissions", a.listPermissions)
	mux.HandleFunc("PUT /api/permissions", a.setPermissions)
	mux.HandleFunc("GET /api/repos/{name}/assets", a.listAssets)
	mux.HandleFunc("GET /api/repos/{name}/status", a.repoStatus)
	mux.HandleFunc("GET /api/repos/{name}/usage", a.repoUsage)
	mux.HandleFunc("GET /api/repos/{name}/snippets", a.artifactSnippets)
	mux.HandleFunc("GET /api/repos/{name}/tree", a.tree)
	mux.HandleFunc("GET /api/repos/{name}/file", a.fileDetails)
	mux.HandleFunc("GET /api/repos/{name}/versions", a.versions)
	mux.HandleFunc("GET /api/repos/{name}/builds", a.builds)
	mux.HandleFunc("POST /api/repos/{name}/files/delete", a.deleteFiles)
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
	a.registerRepoKinds(mux)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusNotFound, "not found")
	})
	return http.NewCrossOriginProtection().Handler(mux)
}

// --- helpers ---

// fail maps service errors to status codes without leaking internals.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalid), errors.Is(err, repos.ErrInvalid), errors.Is(err, auth.ErrPermDeps):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrNotHeld):
		httpx.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, auth.ErrLastPeople):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.As(err, new(*repos.MissingError)):
		httpx.Error(w, http.StatusNotFound, err.Error())
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

// session requires a UI session: 401 without one (anonymous, a password or a read token).
func (a *API) session(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p, ok := a.auth.Identify(w, r, true)
	if !ok {
		return nil, false
	}
	if p == nil || !p.Session {
		auth.SessionRequired(w)
		return nil, false
	}
	return p, true
}

// can requires the UI session of a person who holds the server permission need (or one of
// them, when need names several): 401 without a UI session, 403 without the permission. Outside
// dev mode the session of a person with a server permission exists only after the second
// factor, so a password alone never reaches a permission route.
func (a *API) can(w http.ResponseWriter, r *http.Request, need auth.ServerPerm) (*auth.Principal, bool) {
	p, ok := a.session(w, r)
	if !ok {
		return nil, false
	}
	if p.Perms&need == 0 {
		httpx.Error(w, http.StatusForbidden, "permission required: "+strings.Join(need.Names(), " or "))
		return nil, false
	}
	return p, true
}

// canRepo requires the UI session of a person who holds the repository permission need (or one
// of them, when need names several) on repo {name}, and loads the repo (404 if missing). The
// All repositories permission holds every repository permission. It returns the caller's whole
// set on the repo.
func (a *API) canRepo(w http.ResponseWriter, r *http.Request, need auth.RepoPerm) (*auth.Principal, *repos.Repo, auth.RepoPerm, bool) {
	p, ok := a.session(w, r)
	if !ok {
		return nil, nil, 0, false
	}
	repo, err := a.repos.ByName(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return nil, nil, 0, false
	}
	set, err := a.auth.RepoPerms(r.Context(), p, repo)
	if err != nil {
		fail(w, r, err)
		return nil, nil, 0, false
	}
	if !set.Any(need) {
		httpx.Error(w, http.StatusForbidden, "permission required on "+repo.Name+": "+strings.Join(need.Names(), " or "))
		return nil, nil, 0, false
	}
	return p, repo, set, true
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

// me returns the caller with their server permissions (permissions) and, by repo name, their
// repository permissions on the repos where they hold more than download (repo_permissions; a
// holder of All repositories holds every one everywhere and gets none listed).
// second_factor_required is true for a UI session of a local account that needs the second
// factor and has no TOTP factor or passkey (outside dev mode): that session reaches only the
// setup routes.
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	p, ok := a.user(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	u, err := a.auth.GetUser(ctx, p.Username)
	if err != nil {
		fail(w, r, err)
		return
	}
	setupOnly, err := a.auth.SetupOnly(ctx, p)
	if err != nil {
		fail(w, r, err)
		return
	}
	sets, err := a.auth.UserRepoPerms(ctx, u.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	held := map[string][]string{}
	for id, set := range sets {
		if set&^auth.RepoDownload == 0 {
			continue
		}
		repo, err := a.repos.ByID(ctx, id)
		if err != nil {
			continue // deleted after the grants were read
		}
		held[repo.Name] = set.Names()
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		loginUser
		RepoPermissions map[string][]string `json:"repo_permissions"`
	}{loginUser{u, setupOnly}, held})
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
	if _, ok := a.can(w, r, auth.PermPeople); !ok {
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

// getUser returns one account by name (people): the person page of the UI.
func (a *API) getUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermPeople); !ok {
		return
	}
	u, err := a.auth.GetUser(r.Context(), r.PathValue("username"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

// permsIn reads a list of server permission names; an unknown name answers 400.
func permsIn(w http.ResponseWriter, names []string) (auth.ServerPerm, bool) {
	set, err := auth.ParseServerPerms(names)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "unknown permission; the server permissions are "+strings.Join(auth.ServerPermNames, ", "))
		return 0, false
	}
	return set, true
}

// createUser creates a local account (people): {username, password, permissions} -> 201. The
// caller gives only server permissions they hold (403 otherwise).
func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	p, ok := a.can(w, r, auth.PermPeople)
	if !ok {
		return
	}
	var in struct {
		Username    string   `json:"username"`
		Password    string   `json:"password"`
		Permissions []string `json:"permissions"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	perms, ok := permsIn(w, in.Permissions)
	if !ok {
		return
	}
	if perms&^p.Perms != 0 {
		httpx.Error(w, http.StatusForbidden, auth.ErrNotHeld.Error())
		return
	}
	u, err := a.auth.CreateUser(r.Context(), in.Username, in.Password, perms)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.record(r, p, audit.UserCreate, nil, u.Username, map[string]any{"permissions": u.Permissions})
	httpx.WriteJSON(w, http.StatusCreated, u)
}

// updateUser changes a password: {password}. A person changes their own; with People, the
// password of a person whose permissions the caller holds all of (403 otherwise).
func (a *API) updateUser(w http.ResponseWriter, r *http.Request) {
	p, ok := a.user(w, r)
	if !ok {
		return
	}
	var in struct {
		Password *string `json:"password"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	name := r.PathValue("username")
	if name != p.Username {
		if !p.Session || !p.Perms.Has(auth.PermPeople) {
			httpx.Error(w, http.StatusForbidden, "permission required: people")
			return
		}
		target, err := a.auth.GetUser(r.Context(), name)
		if err != nil {
			fail(w, r, err)
			return
		}
		if ok, err := a.auth.Covers(r.Context(), p, target); err != nil || !ok {
			if err != nil {
				fail(w, r, err)
			} else {
				httpx.Error(w, http.StatusForbidden, "you can change the password only of a person whose permissions you all hold")
			}
			return
		}
	}
	u, err := a.auth.UpdateUser(r.Context(), name, in.Password)
	if err != nil {
		fail(w, r, err)
		return
	}
	if in.Password != nil {
		a.record(r, p, audit.UserUpdate, nil, u.Username, map[string]any{"password": "changed"}) // never the value
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

// setUserPermissions replaces the server permissions of {username} (people): {permissions} ->
// 200 with the user. The caller adds or removes only permissions they hold (403), and the last
// enabled person with People keeps it (409). One user.permissions event with the old and the
// new set.
func (a *API) setUserPermissions(w http.ResponseWriter, r *http.Request) {
	p, ok := a.can(w, r, auth.PermPeople)
	if !ok {
		return
	}
	var in struct {
		Permissions []string `json:"permissions"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	perms, ok := permsIn(w, in.Permissions)
	if !ok {
		return
	}
	ctx := r.Context()
	name := r.PathValue("username")
	cur, err := a.auth.GetUser(ctx, name)
	if err != nil {
		fail(w, r, err)
		return
	}
	if (cur.Perms^perms)&^p.Perms != 0 {
		httpx.Error(w, http.StatusForbidden, auth.ErrNotHeld.Error())
		return
	}
	old, err := a.auth.SetUserPerms(ctx, name, perms)
	if err != nil {
		fail(w, r, err)
		return
	}
	if old != perms {
		a.record(r, p, audit.UserPermissions, nil, name, map[string]any{"old": old.Names(), "new": perms.Names()})
	}
	u, err := a.auth.GetUser(ctx, name)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

// listPeople returns enabled usernames that start with ?q=, for a person picker: a keyset page
// of at most 20 by username (?after=). It needs people, or access on at least one repository. The avatar of a person is drawn from the username, so only names come back.
func (a *API) listPeople(w http.ResponseWriter, r *http.Request) {
	p, ok := a.session(w, r)
	if !ok {
		return
	}
	if !p.Perms.Has(auth.PermPeople) && !p.Perms.Has(auth.PermRepos) {
		ids, _, err := a.auth.ManagedRepoIDs(r.Context(), p, auth.RepoAccess)
		if err != nil {
			fail(w, r, err)
			return
		}
		if len(ids) == 0 {
			httpx.Error(w, http.StatusForbidden, "permission required: people, or access on a repository")
			return
		}
	}
	q := r.URL.Query()
	names, next, err := a.auth.People(r.Context(), strings.ToLower(strings.TrimSpace(q.Get("q"))), q.Get("after"), min(httpx.Limit(r), 20))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]map[string]string, len(names))
	for i, n := range names {
		items[i] = map[string]string{"username": n}
	}
	httpx.WriteJSON(w, http.StatusOK, list[map[string]string]{items, cursor(next)})
}

// --- repos ---

type repoJSON struct {
	Name          string          `json:"name"`
	Format        string          `json:"format"`
	Description   string          `json:"description"`
	AnonymousRead bool            `json:"anonymous_read"`
	Config        json.RawMessage `json:"config"`
	Permissions   []string        `json:"permissions"` // the caller's repository permissions
	LockedBy      string          `json:"locked_by"`   // why uploads answer 409; "" when they do not
	// The repo counters: every asset row counts. LastPublishedAt is null before the first publish.
	FileCount       int64      `json:"file_count"`
	TotalSize       int64      `json:"total_size"`
	LastPublishedAt *time.Time `json:"last_published_at"`
	Kind            string     `json:"kind"`                   // hosted, proxy or group
	MemberCount     *int       `json:"member_count,omitempty"` // a group: how many members it has
	// GET of a repo (see repoDetail): a group's members, the groups of a member, a proxy's remote.
	Members []memberJSON `json:"members,omitempty"`
	Groups  []string     `json:"groups,omitempty"`
	Proxy   *proxyJSON   `json:"proxy,omitempty"`
}

func toRepoJSON(r *repos.Repo, perms auth.RepoPerm) repoJSON {
	cfg := r.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	out := repoJSON{
		Name: r.Name, Format: r.Format, Description: r.Description, AnonymousRead: r.AnonymousRead, Config: cfg,
		Permissions: perms.Names(), LockedBy: r.LockedBy, FileCount: r.FileCount, TotalSize: r.TotalSize, Kind: r.Kind,
	}
	if r.Kind == repos.KindGroup {
		n := r.MemberCount
		out.MemberCount = &n
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
		perms, err := a.auth.RepoPerms(ctx, p, repo)
		if err != nil {
			fail(w, r, err)
			return
		}
		if perms == 0 {
			continue
		}
		out = append(out, toRepoJSON(repo, perms))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (a *API) createRepo(w http.ResponseWriter, r *http.Request) {
	p, ok := a.can(w, r, auth.PermRepos)
	if !ok {
		return
	}
	var in struct {
		Name          string            `json:"name"`
		Format        string            `json:"format"`
		Description   string            `json:"description"`
		AnonymousRead bool              `json:"anonymous_read"`
		Config        json.RawMessage   `json:"config"`
		Kind          string            `json:"kind"`    // hosted (default), proxy or group
		Members       []string          `json:"members"` // a group's members in read order
		Proxy         *repos.ProxyInput `json:"proxy"`   // a proxy's remote
		Force         bool              `json:"force"`   // a proxy: create also when the remote does not answer
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	ctx := r.Context()
	var repo *repos.Repo
	var err error
	detail := map[string]any{}
	switch in.Kind {
	case "", repos.KindHosted:
		repo, err = a.repos.Create(ctx, in.Name, in.Format, in.AnonymousRead, in.Description, in.Config)
	case repos.KindGroup:
		repo, err = a.repos.CreateGroup(ctx, in.Name, in.Format, in.AnonymousRead, in.Description, in.Members)
		detail["members"] = in.Members
	case repos.KindProxy:
		px, ok := a.checkNewProxy(w, r, in.Format, in.Config, in.Proxy, in.Force)
		if !ok {
			return
		}
		repo, err = a.repos.CreateProxy(ctx, in.Name, in.Format, in.AnonymousRead, in.Description, in.Config, px)
		detail["remote_url"] = px.RemoteURL
	default:
		httpx.Error(w, http.StatusBadRequest, `kind must be "hosted", "proxy" or "group"`)
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	detail["format"], detail["anonymous_read"], detail["kind"] = repo.Format, repo.AnonymousRead, repo.Kind
	a.record(r, p, audit.RepoCreate, repo, repo.Name, detail)
	a.writeRepo(w, r, http.StatusCreated, p, repo, auth.AllRepoPerms)
}

func (a *API) getRepo(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := a.auth.RepoMeta(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	perms, err := a.auth.RepoPerms(r.Context(), p, repo)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.writeRepo(w, r, http.StatusOK, p, repo, perms)
}

// updateRepo changes the description, the format settings or the proxy remote of repo {name}
// (settings).
func (a *API) updateRepo(w http.ResponseWriter, r *http.Request) {
	p, cur, perms, ok := a.canRepo(w, r, auth.RepoSettings)
	if !ok {
		return
	}
	var in struct {
		Description *string           `json:"description"`
		Config      json.RawMessage   `json:"config"`
		Proxy       *repos.ProxyInput `json:"proxy"` // a proxy's remote; the password is write-only
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	if in.Proxy != nil {
		// The remote changes first: a refused change leaves the repo as it was.
		repo := cur
		old, px, err := a.repos.UpdateProxy(r.Context(), repo, *in.Proxy)
		if err != nil {
			fail(w, r, err)
			return
		}
		if changed := proxyChanges(old, px); len(changed) > 0 {
			a.record(r, p, audit.RepoUpdate, repo, repo.Name, map[string]any{"proxy": changed})
		}
	}
	old, repo, err := a.repos.Update(r.Context(), r.PathValue("name"), in.Description, in.Config)
	if err != nil {
		fail(w, r, err)
		return
	}
	var changed []string
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
	a.writeRepo(w, r, http.StatusOK, p, repo, perms)
}

// --- permissions ---

// listPermissions returns the permission set of each person with a grant on repo {name}
// (access): [{username, perms}] by username.
func (a *API) listPermissions(w http.ResponseWriter, r *http.Request) {
	_, repo, _, ok := a.canRepo(w, r, auth.RepoAccess)
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
	repo, _, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.RepoDownload, true)
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

	ids, isAll, err := a.auth.DownloadableRepoIDs(ctx, p)
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
