package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/nexus"
)

// registerNexus mounts the Nexus source API (spec §12). Every route is admin-only.
func (a *API) registerNexus(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/nexus/sources", a.createNexusSource)
	mux.HandleFunc("GET /api/nexus/sources", a.listNexusSources)
	mux.HandleFunc("GET /api/nexus/sources/{id}", a.getNexusSource)
	mux.HandleFunc("PATCH /api/nexus/sources/{id}", a.updateNexusSource)
	mux.HandleFunc("DELETE /api/nexus/sources/{id}", a.deleteNexusSource)
	mux.HandleFunc("PUT /api/nexus/sources/{id}/repos", a.setNexusRepos)
	mux.HandleFunc("POST /api/nexus/sources/{id}/sync", a.syncNexusSource)
	mux.HandleFunc("POST /api/nexus/sources/{id}/cutover", a.cutOverNexusSource)
	mux.HandleFunc("GET /api/nexus/sources/{id}/syncs", a.listNexusSyncs)
	mux.HandleFunc("GET /api/nexus/syncs/{id}", a.getNexusSync)
	mux.HandleFunc("GET /api/nexus/syncs/{id}/failures", a.nexusSyncFailures)
	mux.HandleFunc("GET /api/nexus/syncs/{id}/log", a.nexusSyncLog)
	mux.HandleFunc("DELETE /api/nexus/syncs/{id}", a.cancelNexusSync)
}

// nexusFail maps Nexus service errors to status codes. A Nexus error is 502, or 400 when Nexus
// rejected the credentials; its message never holds them.
func nexusFail(w http.ResponseWriter, r *http.Request, err error) {
	var se *nexus.SourceError
	switch {
	case errors.As(err, &se) && se.Unauthorized():
		httpx.Error(w, http.StatusBadRequest, se.Error())
	case errors.As(err, &se):
		httpx.Error(w, http.StatusBadGateway, se.Error())
	case errors.Is(err, nexus.ErrInvalid):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, nexus.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, nexus.ErrExists), errors.Is(err, nexus.ErrRunning), errors.Is(err, nexus.ErrNotRunning),
		errors.Is(err, nexus.ErrCutOver), errors.Is(err, nexus.ErrMapped):
		httpx.Error(w, http.StatusConflict, err.Error())
	default:
		fail(w, r, err)
	}
}

// nexusID parses the {id} path value; it writes 400 and returns false when it is not an id.
func nexusID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

// idCursor parses the ?after= keyset cursor of an id-ordered list; 0 when absent.
func idCursor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	s := r.URL.Query().Get("after")
	if s == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		httpx.Error(w, http.StatusBadRequest, "invalid cursor")
		return 0, false
	}
	return n, true
}

// createNexusSource checks the connection and stores the source: 201 with the Source.
func (a *API) createNexusSource(w http.ResponseWriter, r *http.Request) {
	p, ok := a.can(w, r, auth.PermNexus)
	if !ok {
		return
	}
	var in nexus.NewSource
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	src, err := a.nexus.CreateSource(r.Context(), p.UserID, in)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, src)
}

// listNexusSources returns a keyset page of the sources by name: ?after=<name>&limit=.
func (a *API) listNexusSources(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermNexus); !ok {
		return
	}
	items, next, err := a.nexus.Sources(r.Context(), r.URL.Query().Get("after"), httpx.Limit(r))
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list[nexus.Source]{items, cursor(next)})
}

// getNexusSource returns the source with its Nexus repos and their mapping.
func (a *API) getNexusSource(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermNexus); !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	d, err := a.nexus.Detail(r.Context(), id)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

// updateNexusSource replaces the base URL or the credentials after a connection check.
func (a *API) updateNexusSource(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermNexus); !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	var in nexus.SourceUpdate
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	src, err := a.nexus.UpdateSource(r.Context(), id, in)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, src)
}

// deleteNexusSource deletes a cut-over source or one without mapped repos: 204.
func (a *API) deleteNexusSource(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermNexus); !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	if err := a.nexus.DeleteSource(r.Context(), id); err != nil {
		nexusFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setNexusRepos replaces the source's mapping, starts a sync when the mapping holds a repo,
// and returns the SourceDetail (running_sync_id names that sync).
func (a *API) setNexusRepos(w http.ResponseWriter, r *http.Request) {
	p, ok := a.can(w, r, auth.PermNexus)
	if !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	var in nexus.SetReposInput
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	d, err := a.nexus.SetRepos(r.Context(), id, p.UserID, in.Repos)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

// syncNexusSource starts a sync: 202 with the Sync.
func (a *API) syncNexusSource(w http.ResponseWriter, r *http.Request) {
	p, ok := a.can(w, r, auth.PermNexus)
	if !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	sy, err := a.nexus.StartSync(r.Context(), id, p.UserID)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, sy)
}

// cutOverNexusSource starts the final sync of a cut-over: 202 with the Sync (trigger cutover).
func (a *API) cutOverNexusSource(w http.ResponseWriter, r *http.Request) {
	p, ok := a.can(w, r, auth.PermNexus)
	if !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	sy, err := a.nexus.CutOver(r.Context(), id, p.UserID)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, sy)
}

// listNexusSyncs returns a keyset page of the source's syncs, newest first: ?after=<sync
// id>&limit=. The items have no repos.
func (a *API) listNexusSyncs(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermNexus); !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	after, ok := idCursor(w, r)
	if !ok {
		return
	}
	items, next, err := a.nexus.Syncs(r.Context(), id, after, httpx.Limit(r))
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	out := list[nexus.Sync]{Items: items}
	if next > 0 {
		out.Next = cursor(strconv.FormatInt(next, 10))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// getNexusSync returns a sync with its repos.
func (a *API) getNexusSync(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermNexus); !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	sy, err := a.nexus.Sync(r.Context(), id)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sy)
}

// nexusSyncFailures returns a keyset page of a sync's failures: ?after=<cursor>&limit=.
func (a *API) nexusSyncFailures(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermNexus); !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	after, ok := idCursor(w, r)
	if !ok {
		return
	}
	items, next, err := a.nexus.Failures(r.Context(), id, after, httpx.Limit(r))
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	out := list[nexus.Failure]{Items: items}
	if next > 0 {
		out.Next = cursor(strconv.FormatInt(next, 10))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// nexusSyncLog returns a keyset page of a sync's log lines, oldest first:
// ?after=<line id>&limit=&repo=<Nexus repo>.
func (a *API) nexusSyncLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermNexus); !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	after, ok := idCursor(w, r)
	if !ok {
		return
	}
	items, next, err := a.nexus.Log(r.Context(), id, after, r.URL.Query().Get("repo"), httpx.Limit(r))
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	out := list[nexus.LogLine]{Items: items}
	if next > 0 {
		out.Next = cursor(strconv.FormatInt(next, 10))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// cancelNexusSync stops a running sync and returns it (state cancelled); 409 when it has ended.
func (a *API) cancelNexusSync(w http.ResponseWriter, r *http.Request) {
	p, ok := a.can(w, r, auth.PermNexus)
	if !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	sy, err := a.nexus.Cancel(r.Context(), id, p.UserID)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sy)
}
