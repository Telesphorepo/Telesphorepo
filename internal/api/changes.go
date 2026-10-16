package api

import (
	"errors"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/changelog"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// changes answers GET /api/changes?since=<version> (public): {version, changes}. version is the
// version of the server; changes are the released entries of the embedded change log that are
// newer than since, in the order of the file (every released entry without since). A since that
// is not a release version answers 400.
func (a *API) changes(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	all, err := changelog.Embedded()
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	out, err := changelog.Since(all, r.URL.Query().Get("since"))
	if errors.Is(err, changelog.ErrVersion) {
		httpx.Error(w, r, http.StatusBadRequest, "since: "+err.Error())
		return
	}
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Version string             `json:"version"`
		Changes []changelog.Change `json:"changes"`
	}{a.h.Version, out})
}
