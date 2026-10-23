package api

import (
	"net/http"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// registerRedirects mounts the rename of a repo and its redirects.
func (a *API) registerRedirects(mux *route.Router) {
	mux.Handle("POST /api/repos/{name}/rename", route.UIPerm(perm.RepositoriesAll), a.renameRepo)
	mux.Handle("GET /api/repos/{name}/redirects", route.UIRepo(access.Meta), a.listRedirects)
	mux.Handle("DELETE /api/repos/{name}/redirects/{redirect}", route.UIPerm(perm.RepositoriesAll), a.removeRedirect)
}

// renameRepo gives repo {name} a new name: {name} -> 200 with the repo. The old name becomes a
// redirect, so every client that uses it keeps working. 409 when the name is taken by a repo or
// by a redirect of another repo.
func (a *API) renameRepo(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Name string `json:"name"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	repo, err := a.repos.Rename(r.Context(), r.PathValue("name"), in.Name)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	a.writeRepo(w, r, http.StatusOK, c.P, repo, perm.Current().AllRepo())
}

type redirectJSON struct {
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"` // null: never used
}

// listRedirects returns the redirects of repo {name} by name: [{name, created_at, last_used_at}].
// last_used_at counts the uses that the server has not written yet.
func (a *API) listRedirects(w http.ResponseWriter, r *http.Request, c *route.Call) {
	rds, err := a.repos.Redirects(r.Context(), c.Repo.ID)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	out := make([]redirectJSON, len(rds))
	for i, rd := range rds {
		out[i] = redirectJSON{Name: rd.Name, CreatedAt: rd.CreatedAt.UTC()}
		used := rd.LastUsedAt
		if t := a.h.Downloads.RedirectUsed(rd.Name); t.After(used) {
			used = t
		}
		if !used.IsZero() {
			u := used.UTC()
			out[i].LastUsedAt = &u
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// removeRedirect deletes the redirect {redirect} of repo {name} -> 204. The name is free again;
// requests to it answer 404 from then on.
func (a *API) removeRedirect(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	if err := a.repos.RemoveRedirect(r.Context(), r.PathValue("name"), r.PathValue("redirect")); err != nil {
		route.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
