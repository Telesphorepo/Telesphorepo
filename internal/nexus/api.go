package nexus

import (
	"errors"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// nexusFail maps Nexus service errors to status codes. A Nexus error is 502, or 400 when Nexus
// rejected the credentials; its message never holds them.
func nexusFail(w http.ResponseWriter, r *http.Request, err error) {
	var se *SourceError
	var taken *TakenError
	switch {
	case errors.As(err, &taken):
		httpx.WriteProblem(w, r, httpx.Blank(http.StatusConflict, taken.Error()).With(map[string]any{"repo": taken.Name, "rehearsal": taken.Rehearsal}))
	case errors.As(err, &se) && se.Unauthorized():
		httpx.Error(w, r, http.StatusBadRequest, se.Error())
	case errors.As(err, &se):
		httpx.Error(w, r, http.StatusBadGateway, se.Error())
	case errors.Is(err, ErrInvalid):
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not found")
	case errors.Is(err, ErrExists), errors.Is(err, ErrRunning), errors.Is(err, ErrNotRunning),
		errors.Is(err, ErrCutOver), errors.Is(err, ErrMapped), errors.Is(err, ErrSwitched), errors.Is(err, ErrBusy):
		httpx.Error(w, r, http.StatusConflict, err.Error())
	default:
		route.Fail(w, r, err)
	}
}

// createNexusSource checks the connection and stores the source: 201 with the Source.
func (m *mod) createNexusSource(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	var in NewSource
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	src, err := m.svc.CreateSource(r.Context(), p.UserID, in)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, src)
}

// listNexusSources returns a keyset page of the sources by name: ?after=<name>&limit=.
func (m *mod) listNexusSources(w http.ResponseWriter, r *http.Request, c *route.Call) {
	items, next, err := m.svc.Sources(r.Context(), r.URL.Query().Get("after"), httpx.Limit(r))
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[Source]{Items: items, Next: route.Cursor(next)})
}

// getNexusSource returns the source with its Nexus repos and their mapping.
func (m *mod) getNexusSource(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	d, err := m.svc.Detail(r.Context(), id)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

// updateNexusSource replaces the base URL or the credentials after a connection check.
func (m *mod) updateNexusSource(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	var in SourceUpdate
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	src, err := m.svc.UpdateSource(r.Context(), id, in)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, src)
}

// deleteNexusSource deletes a cut-over source or one without mapped repos: 204.
func (m *mod) deleteNexusSource(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	if err := m.svc.DeleteSource(r.Context(), id); err != nil {
		nexusFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setNexusRepos replaces the source's mapping, starts a sync when the mapping holds a repo,
// and returns the SourceDetail (running_sync_id names that sync).
func (m *mod) setNexusRepos(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	var in SetReposInput
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	d, err := m.svc.SetRepos(r.Context(), id, p.UserID, in.Repos)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

// syncNexusSource starts a sync: 202 with the Sync.
func (m *mod) syncNexusSource(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	sy, err := m.svc.StartSync(r.Context(), id, p.UserID)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, sy)
}

// cutOverNexusSource starts the final sync of a cut-over: 202 with the Sync (trigger cutover).
func (m *mod) cutOverNexusSource(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	sy, err := m.svc.CutOver(r.Context(), id, p.UserID)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, sy)
}

// listNexusSyncs returns a keyset page of the source's syncs, newest first: ?after=<sync
// id>&limit=. The items have no repos.
func (m *mod) listNexusSyncs(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	after, ok := route.IDCursor(w, r)
	if !ok {
		return
	}
	items, next, err := m.svc.Syncs(r.Context(), id, after, httpx.Limit(r))
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	out := route.Page[Sync]{Items: items, Next: route.IDNext(next)}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// getNexusSync returns a sync with its repos.
func (m *mod) getNexusSync(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	sy, err := m.svc.Sync(r.Context(), id)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sy)
}

// nexusSyncFailures returns a keyset page of a sync's failures: ?after=<cursor>&limit=.
func (m *mod) nexusSyncFailures(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	after, ok := route.IDCursor(w, r)
	if !ok {
		return
	}
	items, next, err := m.svc.Failures(r.Context(), id, after, httpx.Limit(r))
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	out := route.Page[Failure]{Items: items, Next: route.IDNext(next)}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// nexusSyncLog returns a keyset page of a sync's log lines, oldest first:
// ?after=<line id>&limit=&repo=<Nexus repo>.
func (m *mod) nexusSyncLog(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	after, ok := route.IDCursor(w, r)
	if !ok {
		return
	}
	items, next, err := m.svc.Log(r.Context(), id, after, r.URL.Query().Get("repo"), httpx.Limit(r))
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	out := route.Page[LogLine]{Items: items, Next: route.IDNext(next)}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// cancelNexusSync stops a running sync and returns it (state cancelled); 409 when it has ended.
func (m *mod) cancelNexusSync(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	sy, err := m.svc.Cancel(r.Context(), id, p.UserID)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sy)
}

// switchOverNexusRepo starts the switch-over of the repository that Nexus repo {repo} copies
// into: 202 with its final sync (trigger cutover). 409 when a sync runs, the repository switched
// over already or the source is cut over.
func (m *mod) switchOverNexusRepo(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	sy, err := m.svc.SwitchOverRepo(r.Context(), id, r.PathValue("repo"), c.P.UserID)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, sy)
}

// rehearseNexusRepo creates the rehearsal repository of the repository that Nexus repo {repo}
// copies into: 201 {name}. 409 {repo, rehearsal} when the name is taken; rehearsal is true when
// that repository is its rehearsal already.
func (m *mod) rehearseNexusRepo(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	repo, err := m.svc.Rehearse(r.Context(), id, r.PathValue("repo"), c.P.UserID)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"name": repo.Name})
}

// emptyRehearsal starts to delete every file of rehearsal repository {name}: 202 {name, files}
// with the file count when it started; the repository's operation shows the progress. 400 for a
// repository that rehearses nothing, 409 while it has an operation.
func (m *mod) emptyRehearsal(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo, err := m.svc.Empty(r.Context(), r.PathValue("name"), c.P.UserID)
	if err != nil {
		nexusFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"name": repo.Name, "files": repo.FileCount})
}
