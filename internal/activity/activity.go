// Package activity is the audit module: the API of the audit log (GET /api/audit and its action
// list), the permission that reads it, and the job that deletes events after the audit.retention
// setting. The kernel writes the log (internal/audit).
package activity

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// PermRead reads the audit log.
const PermRead perm.ID = "activity.read"

// Retention is the setting audit.retention.
var Retention = settings.Duration(&settings.Def{Key: "audit.retention", Default: "365d", Label: "Audit log retention",
	Help: "The audit log keeps each event for this time. A job deletes older events every hour."})

// Module returns the audit module.
func Module() module.Module {
	var h *module.Host
	return module.Module{
		ID: "audit", Title: "Activity",
		Register: func(x *module.Extender) {
			x.Permissions(perm.Subsystem{ID: "activity", Scope: perm.ScopeServer, Label: "Activity",
				Help: "The audit log: who did what, when, and from which address."},
				perm.Def{ID: PermRead, Label: "Read the audit log", Help: "See every event of the audit log, with the client addresses.", SecondFactor: true})
			x.Settings(&settings.Group{ID: "activity", Label: "Activity", Perm: perm.SettingsGeneral,
				Defs: []*settings.Def{Retention.Def()}})
			x.Init(func(_ context.Context, host *module.Host) error {
				h = host
				return nil
			})
			x.Job(jobs.Job{Name: "audit.retention", Every: time.Hour, Tick: func(ctx context.Context) error {
				return h.Audit.Cleanup(ctx, Retention.Get(h.Settings.Current()))
			}})
			x.Routes(func(r *route.Router) {
				r.Handle("GET /api/audit", route.UIPerm(PermRead), func(w http.ResponseWriter, r *http.Request, _ *route.Call) { list(h, w, r) })
				r.Handle("GET /api/audit/actions", route.UIPerm(PermRead), func(w http.ResponseWriter, r *http.Request, _ *route.Call) {
					httpx.WriteJSON(w, http.StatusOK, h.Audit.Actions())
				})
			})
		},
	}
}

// list returns the audit log, newest first: ?after=<id>&limit=&action=&repo=<name>.
func list(h *module.Host, w http.ResponseWriter, r *http.Request) {
	after, ok := route.IDCursor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := audit.Filter{Action: q.Get("action")}
	if name := q.Get("repo"); name != "" {
		repo, err := h.Repos.ByName(r.Context(), name)
		if errors.Is(err, repos.ErrNotFound) {
			httpx.WriteJSON(w, http.StatusOK, route.Page[audit.Entry]{Items: []audit.Entry{}})
			return
		}
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		f.RepoID = repo.ID
	}
	items, next, err := h.Audit.List(r.Context(), f, after, httpx.Limit(r))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[audit.Entry]{Items: items, Next: route.IDNext(next)})
}
