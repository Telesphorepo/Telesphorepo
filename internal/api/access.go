package api

import (
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
)

// Effective access (docs/design/information-architecture.md): who can download from and publish
// to each repo, and which repos a person can use.

func (a *API) registerAccess(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repos/{name}/access", a.repoAccess)
	mux.HandleFunc("GET /api/users/{username}/access", a.userAccess)
	mux.HandleFunc("GET /api/me/access", a.myAccess)
}

// repoAccess returns the effective access of repo {name} (admin): {repo, format, download,
// publish}. ?admins=1 lists the admins among the people too.
func (a *API) repoAccess(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.adminRepo(w, r)
	if !ok {
		return
	}
	out, err := a.access.Repo(r.Context(), repo, r.URL.Query().Get("admins") == "1")
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// userAccessJSON is a user's access: the user, and a keyset page of the repos they can use.
type userAccessJSON struct {
	Username   string            `json:"username"`
	IsAdmin    bool              `json:"is_admin"`
	Disabled   bool              `json:"disabled"`
	Kind       string            `json:"kind"`
	CanPublish bool              `json:"can_publish"`
	Items      []access.UserRepo `json:"items"`
	Next       *string           `json:"next"`
}

func (a *API) writeUserAccess(w http.ResponseWriter, r *http.Request, u *auth.User) {
	out, next, err := a.access.User(r.Context(), u, r.URL.Query().Get("after"), httpx.Limit(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, userAccessJSON{out.Username, out.IsAdmin, out.Disabled, out.Kind, out.CanPublish,
		out.Repos, cursor(next)})
}

// userAccess returns the repos user {username} can use (admin): ?after=<repo>&limit=.
func (a *API) userAccess(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	u, err := a.auth.GetUser(r.Context(), r.PathValue("username"))
	if err != nil {
		fail(w, r, err)
		return
	}
	a.writeUserAccess(w, r, u)
}

// myAccess returns the repos the caller can use: ?after=<repo>&limit=.
func (a *API) myAccess(w http.ResponseWriter, r *http.Request) {
	p, ok := a.user(w, r)
	if !ok {
		return
	}
	u, err := a.auth.GetUser(r.Context(), p.Username)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.writeUserAccess(w, r, u)
}
