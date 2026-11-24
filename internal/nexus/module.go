package nexus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/nexus/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/publish"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
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
	// ActionSwitchOverRepo is the start of the switch-over of one repository.
	ActionSwitchOverRepo  = "nexus.repo.switchover"
	ActionRehearsalCreate = "nexus.rehearsal.create"
	ActionRehearsalEmpty  = "nexus.rehearsal.empty"
	ActionMoveCreate      = "nexus.move.create"
	ActionMergeStart      = "nexus.merge.start"
	ActionMergeDone       = "nexus.merge.done"
	ActionMergeFail       = "nexus.merge.fail"
	ActionMergeResume     = "nexus.merge.resume"
	ActionMergeStop       = "nexus.merge.stop"
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
		ID: "nexus", Title: "Nexus migration", Requires: []string{"publish"},
		Register: func(x *module.Extender) {
			x.Schema(component())
			x.Permissions(perm.Subsystem{ID: "nexus", Scope: perm.ScopeServer, Label: "Nexus migration",
				Help: "The copy of repositories from a Sonatype Nexus server, and the switch-over from it."},
				perm.Def{ID: PermManage, Label: "Manage the Nexus migration", Help: "Connect Nexus servers, map their repositories, start and cancel syncs, and cut over.", SecondFactor: true})
			x.AuditActions(auditActions()...)
			x.OpType(ops.Type{ID: OpMirror, Label: "Mirrors a Nexus source",
				Message: func(op *ops.Op) string { return m.svc.mirrorMessage(op) }})
			x.OpType(ops.Type{ID: OpEmpty, Label: "Empties a rehearsal repository", Message: emptyMessage,
				Resume: func(ctx context.Context, op *ops.Op) error { return m.svc.resumeEmpty(ctx, op) },
				Clear:  func(ctx context.Context, op *ops.Op) error { return m.svc.clearEmpty(ctx, op) }})
			x.OpType(ops.Type{ID: OpMerge, Label: "Merges a repository into another",
				Message: func(op *ops.Op) string { return m.svc.mergeMessage(op) },
				Resume:  func(ctx context.Context, op *ops.Op) error { return m.svc.resumeMerge(ctx, op) },
				Clear:   func(ctx context.Context, op *ops.Op) error { return m.svc.clearMerge(ctx, op) }})
			x.RepoField(m.rehearsalField())
			x.RepoField(m.moveField())
			x.Settings(&settings.Group{ID: "nexus", Label: "Nexus migration", Perm: PermManage,
				Defs: []*settings.Def{SyncInterval.Def()}})
			x.Init(func(ctx context.Context, h *module.Host) error {
				m.h = h
				h.Repos.OnPurge(purge(h.DB.W)) // empties the module's rows of a repo in batches before the repo row goes
				m.svc = New(h.DB, h.Settings, h.Repos, h.Assets, h.Ops, h.Formats, h.Audit, h.Log, h.Context(), h.Go)
				m.svc.username = h.Auth.Username
				m.svc.auth = h.Auth
				m.svc.publish = module.Use[*publish.Service](h)
				h.Repos.OnDelete(m.svc.forgetRepo) // a rehearsal's or a move's row cascades with either repo; an id can be used again
				if err := m.svc.loadRehearsals(ctx); err != nil {
					return err
				}
				return m.svc.loadMoves(ctx)
			})
			x.Job(jobs.Job{
				Name: "nexus.scheduler",
				Start: func(ctx context.Context) error {
					if err := m.svc.Resume(ctx); err != nil {
						return err
					}
					if err := m.svc.resumeEmpties(ctx); err != nil {
						return err
					}
					return m.svc.resumeMerges(ctx)
				},
				Run: func(ctx context.Context) { m.svc.Schedule(ctx) },
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
	r.Handle("POST /api/nexus/sources/{id}/repos/{repo}/switch-over", route.UIPerm(PermManage), m.switchOverNexusRepo)
	r.Handle("POST /api/nexus/sources/{id}/repos/{repo}/rehearsal", route.UIPerm(PermManage), m.rehearseNexusRepo)
	r.Handle("POST /api/nexus/rehearsals/{name}/empty", route.UIPerm(PermManage), m.emptyRehearsal)
	r.Handle("POST /api/nexus/sources/{id}/repos/{repo}/move", route.UIPerm(PermManage), m.moveNexusRepo)
	// A merge deletes and renames a repository too, so its routes also need All repositories.
	merge := route.UIPerms(PermManage, perm.RepositoriesAll)
	r.Handle("GET /api/nexus/merges/plan", merge, m.mergePlan)
	r.Handle("POST /api/nexus/merges", merge, m.startMerge)
	r.Handle("GET /api/nexus/merges/{id}", merge, m.getMerge)
}

// rehearsalField is the repo field rehearsal_for. The service exists only after Init, and the
// repo service calls the field only after it.
func (m *mod) rehearsalField() repos.Field {
	return repos.Field{Name: FieldRehearsalFor,
		Applies: func(_, kind string) bool { return kind == repos.KindHosted },
		Validate: func(ctx context.Context, repo *repos.Repo, raw json.RawMessage) (json.RawMessage, error) {
			return m.svc.rehearsalField().Validate(ctx, repo, raw)
		},
		Save: func(ctx context.Context, tx *sql.Tx, repo *repos.Repo, v json.RawMessage) error {
			return m.svc.rehearsalField().Save(ctx, tx, repo, v)
		},
		Load: func(repoID int64) json.RawMessage { return m.svc.rehearsalField().Load(repoID) },
	}
}

// moveField is the repo field moves_into (see rehearsalField).
func (m *mod) moveField() repos.Field {
	return repos.Field{Name: FieldMovesInto,
		Applies: func(_, kind string) bool { return kind == repos.KindHosted },
		Validate: func(ctx context.Context, repo *repos.Repo, raw json.RawMessage) (json.RawMessage, error) {
			return m.svc.moveField().Validate(ctx, repo, raw)
		},
		Save: func(ctx context.Context, tx *sql.Tx, repo *repos.Repo, v json.RawMessage) error {
			return m.svc.moveField().Save(ctx, tx, repo, v)
		},
		Load: func(repoID int64) json.RawMessage { return m.svc.moveField().Load(repoID) },
	}
}

// auditActions are the actions of the module with their sentences and fields.
func auditActions() []audit.ActionDef {
	source := []audit.Field{audit.Hidden("id", "Nexus server"), audit.Text("base_url", "Address")}
	sync := []audit.Field{audit.Hidden("source_id", "Nexus server"), audit.Hidden("sync_id", "Sync")}
	steps := map[string]string{stepSync: "Final sync from Nexus", stepCopy: "Copying the files",
		stepAccess: "Access, CI pipelines and groups", stepSwitch: "Switch-over", stepRetire: "Redirecting the old name",
		stepRename: "Renaming", stepDone: "Done"}
	failedAt := map[string]string{stepSync: "during the final sync from Nexus", stepCopy: "while copying the files",
		stepAccess: "while giving access, CI pipelines and groups", stepSwitch: "while switching over",
		stepRetire: "while redirecting the old name", stepRename: "while renaming", stepDone: "at the end"}
	merge := audit.Hidden("merge_id", "Merge")
	return []audit.ActionDef{
		{ID: ActionSourceCreate, Label: "Nexus source added", Subsystem: "nexus",
			Sentence: "{actor} added the Nexus server {target}", Fields: source},
		{ID: ActionSourceUpdate, Label: "Nexus source changed", Subsystem: "nexus",
			Sentence: "{actor} changed the Nexus server {target}", Fields: source},
		{ID: ActionSourceDelete, Label: "Nexus source deleted", Subsystem: "nexus",
			Sentence: "{actor} removed the Nexus server {target}", Fields: source[:1]},
		{ID: ActionMappingSave, Label: "Nexus repositories mapped", Subsystem: "nexus",
			Sentence: "{actor} chose the repositories to copy from {target}",
			Fields:   []audit.Field{audit.Hidden("id", "Nexus server"), audit.List("repos", "Repositories")}},
		{ID: ActionSyncStart, Label: "Nexus sync started", Subsystem: "nexus",
			Sentence: "{actor} started a sync from {target}",
			Fields: append(slices.Clone(sync), audit.Enum("trigger", "Started", map[string]string{
				TriggerSave: "after choosing the repositories", TriggerManual: "by hand"}))},
		{ID: ActionSyncCancel, Label: "Nexus sync cancelled", Subsystem: "nexus",
			Sentence: "{actor} cancelled the sync from {target}", Fields: sync},
		{ID: ActionCutover, Label: "Nexus cut-over started", Subsystem: "nexus",
			Sentence: "{actor} started switching over everything from {target}", Fields: sync},
		{ID: ActionSwitchOverRepo, Label: "Nexus repository switch-over started", Subsystem: "nexus",
			Sentence: "{actor} started switching {source_repo} over to {target}",
			Fields:   append(slices.Clone(sync), audit.Text("source_repo", "Nexus repository"))},
		{ID: ActionRehearsalCreate, Label: "Rehearsal repository created", Subsystem: "nexus",
			Sentence: "{actor} created the rehearsal repository {target} for {for}",
			Fields:   []audit.Field{audit.Text("for", "Rehearsal for"), audit.List("people", "Can publish").Quietly()}},
		{ID: ActionRehearsalEmpty, Label: "Rehearsal repository emptied", Subsystem: "nexus",
			Sentence: "{actor} emptied the rehearsal repository {repo}",
			Fields:   []audit.Field{audit.Count("count", "Files", "file", "files")}},
		{ID: ActionMoveCreate, Label: "Repository created to move project by project", Subsystem: "nexus",
			Sentence: "{actor} created {target} to move projects into {into} one at a time",
			Fields: []audit.Field{
				audit.Text("into", "Moves into"),
				audit.Text("group", "Group that reads both").Quietly(),
				audit.List("people", "Can publish").Quietly(),
			}},
		{ID: ActionMergeStart, Label: "Merge started", Subsystem: "nexus",
			Sentence: "{actor} started merging {from} into {into}",
			Fields: []audit.Field{merge, audit.Text("from", "From"), audit.Text("into", "Into"),
				audit.Count("files", "Files", "file", "files"),
				audit.Count("conflicts", "Conflicts", "conflict", "conflicts").Quietly(),
				audit.Enum("keep", "Name kept", map[string]string{KeepTarget: "The name it goes into", KeepSource: "The name it comes from"}).Quietly(),
			}},
		{ID: ActionMergeDone, Label: "Merge done", Subsystem: "nexus",
			Sentence: "Merge of {from} into {into} finished",
			Fields: []audit.Field{merge, audit.Text("from", "From"), audit.Text("into", "Into"),
				audit.Count("copied", "Files copied", "file copied", "files copied"),
				audit.Text("name", "Name now").Quietly(),
				audit.Count("skipped", "Files already there", "file already there", "files already there").Quietly(),
				audit.Count("rebuilt", "Files rebuilt", "file rebuilt", "files rebuilt").Quietly(),
				audit.Count("kept", "Conflicts that kept the file there", "file kept", "files kept").Quietly(),
				audit.Count("taken", "Conflicts that took the merged file", "file taken over", "files taken over").Quietly(),
			}},
		{ID: ActionMergeFail, Label: "Merge failed", Subsystem: "nexus",
			Sentence: "Merge of {target} into {repo} failed",
			Fields:   []audit.Field{merge, audit.Enum("step", "Step", failedAt), audit.Text("error", "Error")}},
		{ID: ActionMergeResume, Label: "Merge resumed", Subsystem: "nexus",
			Sentence: "{actor} resumed the merge of {target} into {repo}",
			Fields:   []audit.Field{merge, audit.Enum("step", "Step", steps).Quietly()}},
		{ID: ActionMergeStop, Label: "Merge stopped", Subsystem: "nexus",
			Sentence: "{actor} stopped the merge of {target} into {repo}",
			Fields:   []audit.Field{merge, audit.Enum("step", "Step", steps).Quietly()}},
	}
}

// sourceName returns the name of Nexus source id for an audit event, or its id when it has none.
func (s *Service) sourceName(ctx context.Context, id int64) string {
	if row, err := dbq.New(s.db.R).GetNexusSource(ctx, id); err == nil {
		return row.Name
	}
	return strconv.FormatInt(id, 10)
}

// record writes an audit event of the nexus module with the actor of ctx.
func (s *Service) record(ctx context.Context, action, target string, detail map[string]any) {
	_ = s.audit.Record(ctx, audit.Event{Action: action, Target: target, Detail: detail})
}
