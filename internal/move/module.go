package move

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// PermManage edits the storage settings and reads the storage moves.
const PermManage perm.ID = "storage.manage"

// The audit actions of the storage module.
const (
	ActionStart  = "storage.move.start"
	ActionCancel = "storage.move.cancel"
	ActionDone   = "storage.move.done"
	ActionFailed = "storage.move.failed"
)

// Module returns the storage module: the storage settings (group "storage") with their owner
// hooks (the probe of a new store, and the move when its location changes), the storage move API
// and the job that resumes a move left running by the previous process.
func Module() module.Module {
	m := &mod{}
	return module.Module{
		ID: "storage", Title: "Storage",
		Register: func(x *module.Extender) {
			x.Schema(component())
			x.Permissions(perm.Subsystem{ID: "storage", Scope: perm.ScopeServer, Label: "Storage",
				Help: "Where the server keeps the files of every repository."},
				perm.Def{ID: PermManage, Label: "Manage storage", Help: "Change the blob store (filesystem or S3) and follow or cancel a storage move.", SecondFactor: true})
			x.AuditActions(
				audit.ActionDef{ID: ActionStart, Label: "Storage move started", Subsystem: "storage"},
				audit.ActionDef{ID: ActionCancel, Label: "Storage move cancelled", Subsystem: "storage"},
				audit.ActionDef{ID: ActionDone, Label: "Storage move done", Subsystem: "storage"},
				audit.ActionDef{ID: ActionFailed, Label: "Storage move failed", Subsystem: "storage"},
			)
			x.Settings(&settings.Group{ID: "storage", Label: "Storage", Perm: PermManage, Page: "/settings/storage",
				Defs: storage.Defs(), Validate: m.validate, Test: m.test, Apply: m.apply})
			x.Init(func(ctx context.Context, h *module.Host) error {
				m.h = h
				m.moves = New(h.DB, h.Settings, h.Storage, h.Blobs, h.Audit, h.Log, h.Context(), h.Go)
				return nil
			})
			x.Job(jobs.Job{Name: "storage.resume", Start: func(ctx context.Context) error { return m.moves.Resume(ctx) }})
			x.Routes(m.routes)
		},
	}
}

type mod struct {
	h     *module.Host
	moves *Service
}

// validate checks the storage settings as a whole (storage.Validate).
func (m *mod) validate(d *settings.Draft, _ settings.Mode) error {
	storage.Validate(d)
	return nil
}

// probe opens the store of cfg and writes, reads and deletes a probe object.
func probe(ctx context.Context, cfg storage.Config) error {
	st, err := storage.Open(ctx, cfg)
	if err != nil {
		return err
	}
	return storage.Probe(ctx, st)
}

// test probes the store that the draft selects (POST /api/settings/storage/test).
func (m *mod) test(ctx context.Context, d *settings.Draft) error {
	if err := probe(ctx, storage.ConfigView.Get(d.Next)); err != nil {
		return &settings.HookError{Status: http.StatusBadRequest, Message: "storage test failed: " + err.Error()}
	}
	return nil
}

// apply runs when a PATCH changes where the blobs are. It refuses the change while a move runs
// (409) and probes the new store (400 with the reason under the key storage). When the change
// points the server at another location and blobs exist, it starts a move: the location
// settings are held until the move is done, and the PATCH answers 202 {move}. A change of the
// same location (other credentials or options) or a server without blobs saves at once.
func (m *mod) apply(ctx context.Context, c *settings.ApplyCall) (*settings.Applied, error) {
	if !c.Changed(storage.LocationKeys...) {
		return nil, nil
	}
	if m.moves.Running() {
		return nil, &settings.HookError{Status: http.StatusConflict, Message: "a storage move is running; wait for it to end or cancel it"}
	}
	target := storage.ConfigView.Get(c.Next)
	if err := probe(ctx, target); err != nil {
		msg := "storage test failed: " + err.Error()
		return nil, &settings.HookError{Status: http.StatusBadRequest, Message: msg, Fields: map[string]string{storage.Backend.Name(): msg}}
	}
	if SameLocation(storage.ConfigView.Get(c.Prev), target) {
		return nil, nil
	}
	has, err := m.moves.HasBlobs(ctx)
	if err != nil || !has {
		return nil, err
	}
	mv, err := m.moves.Start(ctx, target, c.UserID)
	if errors.Is(err, ErrBusy) {
		return nil, &settings.HookError{Status: http.StatusConflict, Message: err.Error()}
	}
	if err != nil {
		return nil, err
	}
	var hold []string
	for k := range c.Patch {
		if slices.Contains(storage.LocationKeys, k) {
			hold = append(hold, k)
		}
	}
	return &settings.Applied{Hold: hold, Status: http.StatusAccepted, Body: map[string]any{"move": mv}}, nil
}

func (m *mod) routes(r *route.Router) {
	r.Handle("GET /api/storage/moves/current", route.UIPerm(PermManage), m.current)
	r.Handle("GET /api/storage/moves/{id}", route.UIPerm(PermManage), m.get)
	r.Handle("GET /api/storage/moves/{id}/failures", route.UIPerm(PermManage), m.failures)
	r.Handle("DELETE /api/storage/moves/{id}", route.UIPerm(PermManage), m.cancel)
}

// fail maps move service errors to status codes.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrBusy), errors.Is(err, ErrNotRunning):
		httpx.Error(w, http.StatusConflict, err.Error())
	default:
		route.Fail(w, r, err)
	}
}

// current returns the newest move, running or finished; 404 when there has been none.
func (m *mod) current(w http.ResponseWriter, r *http.Request, c *route.Call) {
	mv, err := m.moves.Current(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, mv)
}

func (m *mod) get(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid move id")
	if !ok {
		return
	}
	mv, err := m.moves.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, mv)
}

// failures returns a keyset page of a move's failures: ?after=<blob id>&limit=.
func (m *mod) failures(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid move id")
	if !ok {
		return
	}
	after, ok := route.IDCursor(w, r)
	if !ok {
		return
	}
	items, next, err := m.moves.Failures(r.Context(), id, after, httpx.Limit(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[Failure]{Items: items, Next: route.IDNext(next)})
}

// cancel stops a running move and returns it (state cancelled); 409 when it has ended.
func (m *mod) cancel(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid move id")
	if !ok {
		return
	}
	mv, err := m.moves.Cancel(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, mv)
}
