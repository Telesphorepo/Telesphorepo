package api

import (
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/route"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/namesearch"
)

// Effective access: who can download from and publish
// to each repo, and which repos a person can use.

func (a *API) registerAccess(mux *route.Router) {
	mux.Handle("GET /api/repos/{name}/access", route.UIRepo(access.Manage, perm.Current().Managing(perm.ScopeRepo)...), a.repoAccess)
	mux.Handle("GET /api/users/{username}/access", route.UIPerm(perm.PeopleManage), a.userAccess)
	mux.Handle("GET /api/me/access", route.AnyUser, a.myAccess)
}

// repoAccess returns the effective access of repo {name}: {repo, format, kind, people,
// download, publish} (access.Repo). ?admins=1 lists the holders of All repositories among the
// people too; ?q= keeps the people, CI pipelines, IP lists and address ranges that hold it
// (access.Engine.Report). It needs a repository permission that manages the repo (one with a second-factor
// need, such as repo.access, repo.settings or ci.pipelines): the Access tab edits the people, the
// network rules and the CI pipelines.
func (a *API) repoAccess(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	q := r.URL.Query()
	out, err := a.access.Report(r.Context(), repo, q.Get("admins") == "1", namesearch.Text(q.Get("q")))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// userAccessJSON is a user's access: the user, and a keyset page of the repos they can use.
type userAccessJSON struct {
	Username    string            `json:"username"`
	Permissions []string          `json:"permissions"`
	Disabled    bool              `json:"disabled"`
	Kind        string            `json:"kind"`
	CanPublish  bool              `json:"can_publish"`
	Items       []access.UserRepo `json:"items"`
	Next        *string           `json:"next"`
}

func (a *API) writeUserAccess(w http.ResponseWriter, r *http.Request, u *auth.User) {
	q := r.URL.Query()
	if names := q["name"]; len(names) > 0 {
		if len(names) > 200 {
			httpx.Error(w, r, http.StatusBadRequest, "at most 200 names")
			return
		}
		out, err := a.access.UserNamed(r.Context(), u, names)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, userAccessJSON{out.Username, out.Permissions, out.Disabled, out.Kind, out.CanPublish,
			out.Repos, nil})
		return
	}
	out, next, err := a.access.User(r.Context(), u, namesearch.Text(q.Get("q")), q.Get("after"), httpx.Limit(r))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, userAccessJSON{out.Username, out.Permissions, out.Disabled, out.Kind, out.CanPublish,
		out.Repos, route.Cursor(next)})
}

// userAccess returns the repos user {username} can use (people): ?after=<repo>&limit=, and ?q=
// keeps the repos whose name holds it. ?name=a&name=b (at most 200) answers those repos alone,
// with no next page: the grant dialog shows what the person holds on the repos it lists.
func (a *API) userAccess(w http.ResponseWriter, r *http.Request, c *route.Call) {
	u, err := a.auth.GetUser(r.Context(), r.PathValue("username"))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	a.writeUserAccess(w, r, u)
}

// myAccess returns the repos the caller can use: ?after=<repo>&limit=&q=&name=, as userAccess.
func (a *API) myAccess(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	u, err := a.auth.GetUser(r.Context(), p.Username)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	a.writeUserAccess(w, r, u)
}
