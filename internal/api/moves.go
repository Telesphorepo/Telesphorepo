package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/move"
)

// registerMoves mounts the storage move API (spec §11). Every route is admin-only.
func (a *API) registerMoves(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/storage/moves/current", a.currentMove)
	mux.HandleFunc("GET /api/storage/moves/{id}", a.getMove)
	mux.HandleFunc("GET /api/storage/moves/{id}/failures", a.moveFailures)
	mux.HandleFunc("DELETE /api/storage/moves/{id}", a.cancelMove)
}

// moveFail maps move service errors to status codes.
func moveFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, move.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, move.ErrBusy), errors.Is(err, move.ErrNotRunning):
		httpx.Error(w, http.StatusConflict, err.Error())
	default:
		fail(w, r, err)
	}
}

// moveID parses the {id} path value; it writes 400 and returns false when it is not an id.
func moveID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Error(w, http.StatusBadRequest, "invalid move id")
		return 0, false
	}
	return id, true
}

// currentMove returns the newest move, running or finished; 404 when there has been none.
func (a *API) currentMove(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	m, err := a.moves.Current(r.Context())
	if err != nil {
		moveFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m)
}

func (a *API) getMove(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	id, ok := moveID(w, r)
	if !ok {
		return
	}
	m, err := a.moves.Get(r.Context(), id)
	if err != nil {
		moveFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m)
}

// moveFailures returns a keyset page of a move's failures: ?after=<asset id>&limit=.
func (a *API) moveFailures(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	id, ok := moveID(w, r)
	if !ok {
		return
	}
	var after int64
	if s := r.URL.Query().Get("after"); s != "" {
		var err error
		if after, err = strconv.ParseInt(s, 10, 64); err != nil || after < 0 {
			httpx.Error(w, http.StatusBadRequest, "invalid cursor")
			return
		}
	}
	items, next, err := a.moves.Failures(r.Context(), id, after, httpx.Limit(r))
	if err != nil {
		moveFail(w, r, err)
		return
	}
	out := list[move.Failure]{Items: items}
	if next > 0 {
		out.Next = cursor(strconv.FormatInt(next, 10))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// cancelMove stops a running move and returns it (state cancelled); 409 when it has ended.
func (a *API) cancelMove(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	id, ok := moveID(w, r)
	if !ok {
		return
	}
	m, err := a.moves.Cancel(r.Context(), id)
	if err != nil {
		moveFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m)
}
