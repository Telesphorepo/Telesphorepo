// Package api is the core module: the kernel's part of the JSON API under /api/.
// Every other module mounts its own /api/ routes.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/changelog"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/token"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/files"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// API is the core module: sign-in, the account, people, read tokens, repos, grants, files,
// uploads, search, the settings API and the format list.
type API struct {
	h           *module.Host
	kit         *route.API
	auth        *auth.Service
	repos       *repos.Service
	assets      *assets.Pipeline
	reader      *assets.Reader
	uploads     *assets.Uploads
	ops         *ops.Service
	settings    *settings.Service
	files       *files.Service
	audit       *audit.Log
	access      *access.Engine
	formats     *format.Registry
	heads       *heldHeads
	deleteSlots chan struct{} // one value per running repo delete (startDelete)
}

// optimizeEvery is how often the core.optimize job refreshes the planner statistics.
const optimizeEvery = 6 * time.Hour

// Module returns the core module. It registers the kernel's permissions, audit actions and the
// settings group "general", the token kind of read tokens and the job that keeps the planner
// statistics fresh.
func Module() module.Module {
	a := &API{deleteSlots: make(chan struct{}, deleteSlots)}
	return module.Module{
		ID: "core", Title: "Core",
		Register: func(x *module.Extender) {
			register(x)
			x.Init(func(_ context.Context, h *module.Host) error {
				a.h, a.kit = h, h.API
				a.auth, a.repos, a.assets, a.settings = h.Auth, h.Repos, h.Assets, h.Settings
				a.reader, a.uploads, a.ops = h.Reader, h.Uploads, h.Ops
				a.audit, a.access, a.formats = h.Audit, h.Access, h.Formats
				a.files = files.New(h.Assets, h.Auth, h.Builds)
				a.heads = newHeldHeads(func() string { return filepath.Join(a.settings.Current().TmpDir(), "plans") })
				_, err := changelog.Embedded() // a broken change log stops the startup
				return err
			})
			x.TokenKind(token.ReadToken, func(ctx context.Context, publicID, secret string) (*auth.Principal, error) {
				return a.auth.VerifyReadToken(ctx, publicID, secret)
			})
			x.Job(jobs.Job{Name: "core.optimize", Every: optimizeEvery, Tick: func(ctx context.Context) error {
				return a.h.DB.Optimize(ctx)
			}})
			a.registerPipeline(x)
			x.RootPrefix("/identity/") // the logo of the server
			x.Routes(a.routes)
		},
	}
}

// routes mounts the core routes of the JSON API, each with its policy.
func (a *API) routes(mux *route.Router) {
	a.registerSecurity(mux) // sign-in (password, then second factor) and second-factor setup
	mux.Handle("POST /api/auth/logout", route.Setup, a.logout)
	mux.Handle("GET /api/auth/config", route.Public, a.authConfig)
	mux.Handle("GET /api/me", route.Policy{Accept: auth.CredPassword | auth.CredReadToken | auth.CredSession | auth.CredSetupSession}, a.me)

	mux.Handle("GET /api/tokens", route.AnyUser, a.listTokens)
	mux.Handle("POST /api/tokens", route.UI, a.createToken)
	mux.Handle("DELETE /api/tokens/{id}", route.UI, a.deleteToken)

	mux.Handle("GET /api/users", route.UIPerm(perm.PeopleManage), a.listUsers)
	mux.Handle("POST /api/users", route.UIPerm(perm.PeopleManage), a.createUser)
	mux.Handle("PATCH /api/users", route.UIPerm(perm.PeopleManage), a.setUsersDisabled)
	mux.Handle("GET /api/users/{username}", route.UIPerm(perm.PeopleManage), a.getUser)
	mux.Handle("PATCH /api/users/{username}", route.UI, a.updateUser)
	mux.Handle("PUT /api/users/{username}/permissions", route.UIPerm(perm.PeopleManage), a.setUserPermissions)
	mux.Handle("GET /api/people", route.Policy{Accept: auth.CredSession, Perm: perm.PeopleManage, SomeRepo: perm.RepoAccess}, a.listPeople)

	mux.Handle("GET /api/changes", route.Public, a.changes)
	mux.Handle("GET /api/formats", route.Public, a.listFormats)
	mux.Handle("GET /api/permissions", route.Public, a.listPermissionRegistry)

	mux.Handle("GET /api/repos", route.Policy{Accept: auth.CredPassword | auth.CredReadToken | auth.CredSession, Anonymous: true}, a.listRepos)
	mux.Handle("POST /api/repos", route.UIPerm(perm.RepositoriesAll), a.createRepo)
	mux.Handle("PATCH /api/repos", route.UI, a.setReposPublic)
	mux.Handle("POST /api/repos/delete", route.UIPerm(perm.RepositoriesAll), a.deleteRepos)
	mux.Handle("GET /api/repos/{name}", route.Policy{Accept: auth.CredPassword | auth.CredReadToken | auth.CredSession, Anonymous: true,
		Repo: &route.RepoRule{Op: access.Meta}}, a.getRepo)
	mux.Handle("PATCH /api/repos/{name}", route.UIRepo(access.Manage, perm.RepoSettings), a.updateRepo)
	mux.Handle("GET /api/repos/{name}/grants", route.UIRepo(access.Manage, perm.RepoAccess), a.listGrants)
	mux.Handle("PUT /api/grants", route.UI, a.setPermissions)
	mux.Handle("GET /api/repos/{name}/assets", route.RepoRead(), a.listAssets)
	mux.Handle("GET /api/repos/{name}/status", route.RepoRead(), a.repoStatus)
	mux.Handle("GET /api/repos/{name}/usage", route.RepoRead(), a.repoUsage)
	ciRead := route.RepoRead()
	ciRead.Accept |= auth.CredPublish // the publish session of the repo (telesphorepo setup)
	mux.Handle("GET /api/repos/{name}/ci", ciRead, a.repoCI)
	mux.Handle("GET /api/repos/{name}/snippets", route.RepoRead(), a.artifactSnippets)
	mux.Handle("GET /api/repos/{name}/tree", route.RepoRead(), a.tree)
	mux.Handle("GET /api/repos/{name}/file", route.RepoRead(), a.fileDetails)
	mux.Handle("GET /api/repos/{name}/versions", route.RepoRead(), a.versions)
	mux.Handle("GET /api/repos/{name}/builds", route.RepoRead(), a.builds)
	del := route.Policy{Accept: auth.CredSession | auth.CredPublish, Repo: &route.RepoRule{Op: access.Delete, Perm: perm.RepoDelete}}
	mux.Handle("POST /api/repos/{name}/files/delete", del, a.deleteFiles)
	mux.Handle("POST /api/repos/{name}/files/delete/plan", del, a.deletePlan)
	upload := route.Policy{Accept: auth.CredPublish, Repo: &route.RepoRule{Op: access.Write, Perm: perm.RepoPublish}}
	mux.Handle("POST /api/repos/{name}/upload", upload, a.upload)
	mux.Handle("POST /api/repos/{name}/upload/plan", upload, a.uploadPlan)
	mux.Handle("POST /api/repos/{name}/uploads", upload, a.startUpload)
	mux.Handle("PATCH /api/repos/{name}/uploads/{id}", upload, a.appendUpload)
	mux.Handle("PUT /api/repos/{name}/uploads/{id}", upload, a.finishUpload)
	mux.Handle("DELETE /api/repos/{name}/uploads/{id}", upload, a.cancelUpload)
	mux.Handle("POST /api/repos/{name}/operation/resume", route.UIPerm(perm.RepositoriesAll), a.resumeOperation)
	mux.Handle("DELETE /api/repos/{name}/operation", route.UIPerm(perm.RepositoriesAll), a.clearOperation)

	mux.Handle("GET /api/search", route.Policy{Accept: auth.CredPassword | auth.CredReadToken | auth.CredSession, Anonymous: true}, a.search)

	settingsPolicy := route.Policy{Accept: auth.CredSession, AnyPerm: a.settingsPerms()}
	mux.Handle("GET /api/settings", settingsPolicy, a.getSettings)
	mux.Handle("PATCH /api/settings", settingsPolicy, a.patchSettings)
	mux.Handle("POST /api/settings/{group}/test", settingsPolicy, a.testSettings)

	a.registerAccess(mux)
	a.registerRepoKinds(mux)
	a.registerIdentity(mux)
	a.registerRedirects(mux)

	mux.Handle("/api/", route.Public, func(w http.ResponseWriter, r *http.Request, _ *route.Call) {
		httpx.Error(w, r, http.StatusNotFound, "not found")
	})
}

// --- auth ---

func (a *API) logout(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	if err := a.auth.Logout(w, r); err != nil {
		route.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// me returns the session view of the caller (auth.SessionView): the same shape as a finished
// sign-in.
func (a *API) me(w http.ResponseWriter, r *http.Request, c *route.Call) {
	v, err := a.auth.View(r.Context(), c.P.UserID, c.P.Is(auth.CredSetupSession))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

// --- tokens ---

func (a *API) listTokens(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	ts, err := a.auth.ListTokens(r.Context(), p.UserID)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if ts == nil {
		ts = []*auth.Token{}
	}
	httpx.WriteJSON(w, http.StatusOK, ts)
}

func (a *API) createToken(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	// SSO accounts are UI-only.
	if p.Kind != auth.KindLocal {
		httpx.Error(w, r, http.StatusForbidden, "SSO accounts cannot create API tokens")
		return
	}
	var in struct {
		Name          string `json:"name"`
		ExpiresInDays *int   `json:"expires_in_days"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	var exp time.Duration
	if in.ExpiresInDays != nil {
		if d := *in.ExpiresInDays; d < 1 || d > 36500 {
			httpx.Error(w, r, http.StatusBadRequest, "expires_in_days must be between 1 and 36500")
			return
		}
		exp = time.Duration(*in.ExpiresInDays) * 24 * time.Hour
	}
	t, plain, err := a.auth.CreateToken(r.Context(), p.UserID, in.Name, exp)
	if err != nil {
		route.Fail(w, r, err)
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

func (a *API) deleteToken(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid token id")
		return
	}
	if err := a.auth.DeleteToken(r.Context(), p, id); err != nil {
		route.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- users ---

func (a *API) listUsers(w http.ResponseWriter, r *http.Request, c *route.Call) {
	us, next, err := a.auth.ListUsers(r.Context(), r.URL.Query().Get("after"), httpx.Limit(r))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if us == nil {
		us = []*auth.User{}
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[*auth.User]{Items: us, Next: route.Cursor(next)})
}

// getUser returns one account by name (people): the person page of the UI.
func (a *API) getUser(w http.ResponseWriter, r *http.Request, c *route.Call) {
	u, err := a.auth.GetUser(r.Context(), r.PathValue("username"))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

// permsIn reads a list of server permission IDs. An unknown ID or a broken need answers 400.
func permsIn(w http.ResponseWriter, r *http.Request, ids []string) (perm.Set, bool) {
	set, err := perm.Current().Parse(perm.ScopeServer, ids)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
		return perm.Set{}, false
	}
	return set, true
}

// createUser creates a local account (people): {username, password, permissions} -> 201. The
// caller gives only server permissions they hold (403 otherwise).
func (a *API) createUser(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	var in struct {
		Username    string   `json:"username"`
		Password    string   `json:"password"`
		Permissions []string `json:"permissions"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	perms, ok := permsIn(w, r, in.Permissions)
	if !ok {
		return
	}
	if !p.Perms.Contains(perms) {
		httpx.Error(w, r, http.StatusForbidden, auth.ErrNotHeld.Error())
		return
	}
	u, err := a.auth.CreateUser(r.Context(), in.Username, in.Password, perms)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, u)
}

// updateUser changes a password: {password}. A person changes their own; with people.manage, the
// password of a person whose permissions the caller holds all of (403 otherwise).
func (a *API) updateUser(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	var in struct {
		Password *string `json:"password"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	name := r.PathValue("username")
	if name != p.Username {
		if !p.Is(auth.CredSession) || !p.Perms.Has(perm.PeopleManage) {
			httpx.Error(w, r, http.StatusForbidden, "permission required: "+perm.Current().Label(perm.PeopleManage))
			return
		}
		target, err := a.auth.GetUser(r.Context(), name)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		if ok, err := a.auth.Covers(r.Context(), p, target); err != nil || !ok {
			if err != nil {
				route.Fail(w, r, err)
			} else {
				httpx.Error(w, r, http.StatusForbidden, "you can change the password only of a person whose permissions you all hold")
			}
			return
		}
	}
	u, err := a.auth.UpdateUser(r.Context(), name, in.Password)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

// setUserPermissions replaces the server permissions of {username} (people): {permissions} ->
// 200 with the user. The caller adds or removes only permissions they hold (403), and the last
// enabled person with people.manage keeps it (409). The auth service records user.permissions.
func (a *API) setUserPermissions(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	var in struct {
		Permissions []string `json:"permissions"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	perms, ok := permsIn(w, r, in.Permissions)
	if !ok {
		return
	}
	ctx := r.Context()
	name := r.PathValue("username")
	cur, err := a.auth.GetUser(ctx, name)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if cur.PermissionsManaged {
		route.Fail(w, r, auth.ErrManaged)
		return
	}
	if diff := cur.Perms.Minus(perms).Union(perms.Minus(cur.Perms)); !p.Perms.Contains(diff) {
		httpx.Error(w, r, http.StatusForbidden, auth.ErrNotHeld.Error())
		return
	}
	if _, _, err := a.auth.SetUserPerms(ctx, name, in.Permissions); err != nil {
		route.Fail(w, r, err)
		return
	}
	u, err := a.auth.GetUser(ctx, name)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

// listPeople returns enabled usernames that start with ?q=, for a person picker: a keyset page
// of at most 20 by username (?after=). Its policy needs people.manage, or repo.access on at least one
// repository. The avatar of a person is drawn from the username, so only names come back.
func (a *API) listPeople(w http.ResponseWriter, r *http.Request, c *route.Call) {
	q := r.URL.Query()
	names, next, err := a.auth.People(r.Context(), strings.ToLower(strings.TrimSpace(q.Get("q"))), q.Get("after"), min(httpx.Limit(r), 20))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	items := make([]map[string]string, len(names))
	for i, n := range names {
		items[i] = map[string]string{"username": n}
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[map[string]string]{Items: items, Next: route.Cursor(next)})
}

// --- repos ---

type repoJSON struct {
	Name          string          `json:"name"`
	Format        string          `json:"format"`
	Description   string          `json:"description"`
	AnonymousRead bool            `json:"anonymous_read"`
	Config        json.RawMessage `json:"config"`
	// Fields are the values of the module fields of the repo (repos.Field), e.g. signing_key.
	Fields      map[string]json.RawMessage `json:"fields"`
	Permissions []string                   `json:"permissions"` // the caller's repository permissions
	Operation   *operationJSON             `json:"operation"`   // the active operation; null for none
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

// operationJSON is the active operation of a repo: while it runs or holds, writes, deletes and
// mints answer 409 with its message.
type operationJSON struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Owner   string `json:"owner"`
	State   string `json:"state"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Message string `json:"message"`
	Error   string `json:"error"`
}

// toOperationJSON is op as JSON; nil for none.
func (a *API) toOperationJSON(op *ops.Op) *operationJSON {
	if op == nil {
		return nil
	}
	return &operationJSON{ID: op.ID, Type: op.Type, Owner: op.Owner, State: op.State, Done: op.Done, Total: op.Total,
		Message: a.ops.Message(op), Error: op.Error}
}

func (a *API) toRepoJSON(r *repos.Repo, perms perm.Set) repoJSON {
	cfg := r.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	out := repoJSON{
		Name: r.Name, Format: r.Format, Description: r.Description, AnonymousRead: r.AnonymousRead, Config: cfg,
		Fields: r.Fields, Permissions: perms.Strings(), FileCount: r.FileCount, TotalSize: r.TotalSize, Kind: r.Kind,
	}
	if out.Fields == nil {
		out.Fields = map[string]json.RawMessage{}
	}
	out.Operation = a.toOperationJSON(r.Op)
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

func (a *API) listRepos(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	ctx := r.Context()
	all, err := a.repos.List(ctx)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	// One query reads the caller's grants on every repo; a repo without a permission is left out.
	if ctx, err = a.auth.WithGrants(ctx, p); err != nil {
		route.Fail(w, r, err)
		return
	}
	out := []repoJSON{}
	for _, repo := range all {
		perms, err := a.auth.RepoPerms(ctx, p, repo)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		if perms.Empty() {
			continue
		}
		out = append(out, a.toRepoJSON(repo, perms))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (a *API) createRepo(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
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
		// Fields are module field values, e.g. {"signing_key": "default"}; a field without one
		// gets its default.
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	create := repos.Create{Name: in.Name, Format: in.Format, Kind: in.Kind, AnonymousRead: in.AnonymousRead,
		Description: in.Description, Config: in.Config, Members: in.Members, Fields: in.Fields}
	if in.Kind == repos.KindProxy {
		px, ok := a.checkNewProxy(w, r, in.Format, in.Config, in.Proxy, in.Force)
		if !ok {
			return
		}
		create.Proxy = px
	}
	repo, err := a.repos.Create(r.Context(), create)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	a.writeRepo(w, r, http.StatusCreated, p, repo, perm.Current().AllRepo())
}

func (a *API) getRepo(w http.ResponseWriter, r *http.Request, c *route.Call) {
	a.writeRepo(w, r, http.StatusOK, c.P, c.Repo, c.RepoPerms)
}

// updateRepo changes the description, the format settings, the proxy remote or the module field
// values of repo {name} (settings) in one repo update: the whole new state is checked before
// anything is saved.
func (a *API) updateRepo(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Description *string                    `json:"description"`
		Config      json.RawMessage            `json:"config"`
		Proxy       *repos.ProxyInput          `json:"proxy"`  // a proxy's remote; the password is write-only
		Fields      map[string]json.RawMessage `json:"fields"` // module field values to change
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	repo, err := a.repos.Update(r.Context(), c.Repo.Name, repos.Patch{Description: in.Description, Config: in.Config, Proxy: in.Proxy, Fields: in.Fields})
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	a.writeRepo(w, r, http.StatusOK, c.P, repo, c.RepoPerms)
}

// --- permissions ---

// listGrants returns the permission set of each person with a grant on repo {name}
// (repo.access): [{username, perms}] by username.
func (a *API) listGrants(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	ps, err := a.auth.ListPermissions(r.Context(), repo.ID)
	if err != nil {
		route.Fail(w, r, err)
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
	Repo        string    `json:"repo,omitempty"` // a group: the member that serves the file
}

// listAssets lists the files of the repo under ?prefix= in path order: ?after=<path>&limit= ->
// {items: [{path, size, sha256, content_type, kind, updated_at, repo}], next}. A group merges the
// pages of the members that the caller reads, each read from the same cursor: a path comes from
// the first member that has it (named in repo), as a group read serves it. The first limit paths
// of the merge are the first limit of the union, because each member's page holds all its paths
// before its limit-th.
func (a *API) listAssets(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	q := r.URL.Query()
	limit := httpx.Limit(r)
	members := []*repos.Repo{repo}
	if repo.Kind == repos.KindGroup {
		var err error
		if members, err = a.access.GroupMembers(r.Context(), r, c.P, repo); err != nil {
			route.Fail(w, r, err)
			return
		}
	}
	var out []assetJSON
	byPath := map[string]bool{}
	more := false
	for _, m := range members {
		items, next, err := a.assets.List(r.Context(), m.ID, q.Get("prefix"), q.Get("after"), limit)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		more = more || next != ""
		for _, x := range items {
			if byPath[x.Path] {
				continue // an earlier member serves it
			}
			byPath[x.Path] = true
			j := assetJSON{Path: x.Path, Size: x.Size, SHA256: x.SHA256, ContentType: x.ContentType, Kind: x.Kind, UpdatedAt: x.UpdatedAt}
			if m != repo {
				j.Repo = m.Name
			}
			out = append(out, j)
		}
	}
	slices.SortFunc(out, func(x, y assetJSON) int { return strings.Compare(x.Path, y.Path) })
	if len(out) > limit {
		out, more = out[:limit], true
	}
	next := ""
	if more && len(out) > 0 {
		next = out[len(out)-1].Path
	}
	if out == nil {
		out = []assetJSON{}
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[assetJSON]{Items: out, Next: route.Cursor(next)})
}

type hitJSON struct {
	Repo      string    `json:"repo"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Kind      string    `json:"kind"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (a *API) search(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	ctx, qs := r.Context(), r.URL.Query()
	q := qs.Get("q")
	if utf8.RuneCountInString(q) < 3 {
		httpx.Error(w, r, http.StatusBadRequest, "q must be at least 3 characters")
		return
	}
	var after int64
	if s := qs.Get("after"); s != "" {
		var err error
		if after, err = strconv.ParseInt(s, 10, 64); err != nil || after < 0 {
			httpx.Error(w, r, http.StatusBadRequest, "invalid cursor")
			return
		}
	}
	empty := route.Page[hitJSON]{Items: []hitJSON{}}

	ids, isAll, err := a.auth.DownloadableRepoIDs(ctx, p)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if name := qs.Get("repo"); name != "" {
		repo, err := a.repos.ByName(ctx, name)
		if errors.Is(err, repos.ErrNotFound) {
			httpx.WriteJSON(w, http.StatusOK, empty)
			return
		} else if err != nil {
			route.Fail(w, r, err)
			return
		}
		if !isAll && !slices.Contains(ids, repo.ID) {
			httpx.WriteJSON(w, http.StatusOK, empty)
			return
		}
		ids, isAll = []int64{repo.ID}, false
	}
	// Repos whose guards (the read rules of the network module) refuse the caller drop out of
	// the results.
	if denied := a.access.RefusedRepos(r); len(denied) > 0 {
		if isAll {
			all, err := a.repos.List(ctx)
			if err != nil {
				route.Fail(w, r, err)
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
		route.Fail(w, r, err)
		return
	}
	out := route.Page[hitJSON]{Items: make([]hitJSON, 0, len(hits))}
	for _, h := range hits {
		x := h.Asset
		out.Items = append(out.Items, hitJSON{h.RepoName, x.Path, x.Size, x.SHA256, x.Kind, x.UpdatedAt})
	}
	if next > 0 {
		out.Next = route.Cursor(strconv.FormatInt(next, 10))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
