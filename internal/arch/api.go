package arch

import (
	"net/http"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// apiRoutes mounts the JSON routes of the web UI under /api/arch/.
func (h *Handler) apiRoutes(r *route.Router) {
	r.Handle("GET /api/arch/{name}/package", route.RepoRead(), h.packageDetails)
}

// packageDetails answers GET /api/arch/{name}/package?path=: the name, version, architecture and
// the fields of the index entry of the package file path of a hosted repo.
func (h *Handler) packageDetails(w http.ResponseWriter, r *http.Request, c *route.Call) {
	if c.Repo.Format != name {
		httpx.Error(w, r, http.StatusNotFound, "not an Arch repository")
		return
	}
	p, err := repopath.Parse(strings.TrimPrefix(r.URL.Query().Get("path"), "/"))
	if err != nil || !isPackageFile(string(p)) {
		httpx.Error(w, r, http.StatusBadRequest, "path must be a package file of the repository")
		return
	}
	d, err := h.detailsOf(r.Context(), c.Repo, string(p))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if d == nil {
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}
