package nexus

import (
	"context"
	"errors"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// PermManage manages the Nexus sources, their syncs and the cut-over.
const PermManage perm.ID = "nexus.manage"

// The audit actions of the nexus module.
const (
	ActionSourceCreate = "nexus.source.create"
	ActionSourceUpdate = "nexus.source.update"
	ActionSourceDelete = "nexus.source.delete"
	ActionMappingSave  = "nexus.mapping.save"
	ActionSyncStart    = "nexus.sync.start"
	ActionSyncCancel   = "nexus.sync.cancel"
	ActionCutover      = "nexus.cutover"
)

// SyncInterval is the setting nexus.sync_interval.
var SyncInterval = settings.Duration(&settings.Def{Key: "nexus.sync_interval", Default: "15m", Label: "Nexus sync interval",
	Help: "The server syncs each mirroring Nexus source this often, for example every 15m. The minimum is 1m.",
	Validate: func(v any) error {
		if v.(time.Duration) < time.Minute {
			return errors.New("must be at least 1m")
		}
		return nil
	}})

// Module returns the nexus module: Nexus sources, their syncs and the cut-over, the
// sync scheduler, and their API. Every route is for holders of nexus.manage.
func Module() module.Module {
	m := &mod{}
	return module.Module{
		ID: "nexus", Title: "Nexus migration",
		Register: func(x *module.Extender) {
			x.Schema(component())
			x.Permissions(perm.Subsystem{ID: "nexus", Scope: perm.ScopeServer, Label: "Nexus migration",
				Help: "The copy of repositories from a Sonatype Nexus server, and the switch-over from it."},
				perm.Def{ID: PermManage, Label: "Manage the Nexus migration", Help: "Connect Nexus servers, map their repositories, start and cancel syncs, and cut over.", SecondFactor: true})
			x.AuditActions(
				audit.ActionDef{ID: ActionSourceCreate, Label: "Nexus source added", Subsystem: "nexus"},
				audit.ActionDef{ID: ActionSourceUpdate, Label: "Nexus source changed", Subsystem: "nexus"},
				audit.ActionDef{ID: ActionSourceDelete, Label: "Nexus source deleted", Subsystem: "nexus"},
				audit.ActionDef{ID: ActionMappingSave, Label: "Nexus repositories mapped", Subsystem: "nexus"},
				audit.ActionDef{ID: ActionSyncStart, Label: "Nexus sync started", Subsystem: "nexus"},
				audit.ActionDef{ID: ActionSyncCancel, Label: "Nexus sync cancelled", Subsystem: "nexus"},
				audit.ActionDef{ID: ActionCutover, Label: "Nexus cut-over started", Subsystem: "nexus"},
			)
			x.OpType(ops.Type{ID: OpMirror, Label: "Mirrors a Nexus source",
				Message: func(op *ops.Op) string { return m.svc.mirrorMessage(op) }})
			x.Settings(&settings.Group{ID: "nexus", Label: "Nexus migration", Perm: PermManage,
				Defs: []*settings.Def{SyncInterval.Def()}})
			x.Init(func(ctx context.Context, h *module.Host) error {
				m.h = h
				h.Repos.OnPurge(purge(h.DB.W)) // empties the module's rows of a repo in batches before the repo row goes
				m.svc = New(h.DB, h.Settings, h.Repos, h.Assets, h.Ops, h.Formats, h.Audit, h.Log, h.Context(), h.Go)
				m.svc.username = h.Auth.Username
				return nil
			})
			x.Job(jobs.Job{
				Name:  "nexus.scheduler",
				Start: func(ctx context.Context) error { return m.svc.Resume(ctx) },
				Run:   func(ctx context.Context) { m.svc.Schedule(ctx) },
			})
			x.Routes(m.routes)
		},
	}
}

type mod struct {
	h   *module.Host
	svc *Service
}

func (m *mod) routes(r *route.Router) {
	r.Handle("POST /api/nexus/sources", route.UIPerm(PermManage), m.createNexusSource)
	r.Handle("GET /api/nexus/sources", route.UIPerm(PermManage), m.listNexusSources)
	r.Handle("GET /api/nexus/sources/{id}", route.UIPerm(PermManage), m.getNexusSource)
	r.Handle("PATCH /api/nexus/sources/{id}", route.UIPerm(PermManage), m.updateNexusSource)
	r.Handle("DELETE /api/nexus/sources/{id}", route.UIPerm(PermManage), m.deleteNexusSource)
	r.Handle("PUT /api/nexus/sources/{id}/repos", route.UIPerm(PermManage), m.setNexusRepos)
	r.Handle("POST /api/nexus/sources/{id}/sync", route.UIPerm(PermManage), m.syncNexusSource)
	r.Handle("POST /api/nexus/sources/{id}/cutover", route.UIPerm(PermManage), m.cutOverNexusSource)
	r.Handle("GET /api/nexus/sources/{id}/syncs", route.UIPerm(PermManage), m.listNexusSyncs)
	r.Handle("GET /api/nexus/syncs/{id}", route.UIPerm(PermManage), m.getNexusSync)
	r.Handle("GET /api/nexus/syncs/{id}/failures", route.UIPerm(PermManage), m.nexusSyncFailures)
	r.Handle("GET /api/nexus/syncs/{id}/log", route.UIPerm(PermManage), m.nexusSyncLog)
	r.Handle("DELETE /api/nexus/syncs/{id}", route.UIPerm(PermManage), m.cancelNexusSync)
}

// record writes an audit event of the nexus module with the actor of ctx.
func (s *Service) record(ctx context.Context, action, target string, detail map[string]any) {
	_ = s.audit.Record(ctx, audit.Event{Action: action, Target: target, Detail: detail})
}
