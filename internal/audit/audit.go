// Package audit writes and lists the audit log: who did what, to which repo, from which
// address. The audit log is the only place that shows client IP addresses.
//
// Every action has a registered ID (ActionDef): each module registers the actions it records,
// and Record refuses an action that no module registered. The actor travels in the context: the
// route middleware puts the caller there (WithActor), a job uses System and the command line
// CLI, so a service records its own events without a request.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// The actions that the kernel records. The core module registers them.
const (
	SignInSuccess    = "signin.success"
	SignInFailed     = "signin.failed"
	TokenCreate      = "token.create"
	TokenDelete      = "token.delete"
	UserCreate       = "user.create"
	UserUpdate       = "user.update"
	UserPermissions  = "user.permissions" // a server permission set changed (detail old, new)
	FileUpload       = "file.upload"
	FileDelete       = "file.delete"
	GrantSet         = "grant.set" // a repository permission set changed (detail old, new)
	RepoCreate       = "repo.create"
	RepoUpdate       = "repo.update"
	RepoDelete       = "repo.delete"
	RepoDeleteResume = "repo.delete.resume" // a failed repo delete started again
	RepoDeleteClear  = "repo.delete.clear"  // a failed repo delete ended; the repo stays
	SettingsUpdate   = "settings.update"
)

// ActionDef is one action of the audit log.
type ActionDef struct {
	ID        string `json:"id"`        // a dotted name, unique across modules, e.g. "ci.pipeline.create"
	Label     string `json:"label"`     // "CI pipeline created"
	Subsystem string `json:"subsystem"` // a registered permission subsystem, for grouping in the Activity filter
}

var actionRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)+$`)

// Actions is the registry of actions. It is filled while the modules register and then fixed.
type Actions struct {
	defs  []ActionDef
	index map[string]int
}

// NewActions returns an empty action registry.
func NewActions() *Actions { return &Actions{index: map[string]int{}} }

// Add registers defs. subsystem reports whether a permission subsystem is registered. It refuses
// a malformed or repeated ID, an empty label and an unknown subsystem.
func (a *Actions) Add(defs []ActionDef, subsystem func(id string) bool) error {
	for _, d := range defs {
		if !actionRE.MatchString(d.ID) || d.Label == "" {
			return fmt.Errorf("audit: action %q needs a dotted ID and a label", d.ID)
		}
		if _, taken := a.index[d.ID]; taken {
			return fmt.Errorf("audit: action %q is registered twice", d.ID)
		}
		if !subsystem(d.Subsystem) {
			return fmt.Errorf("audit: action %q: subsystem %q is not registered", d.ID, d.Subsystem)
		}
		a.index[d.ID] = len(a.defs)
		a.defs = append(a.defs, d)
	}
	return nil
}

// List returns every action in registration order.
func (a *Actions) List() []ActionDef { return a.defs }

// Known reports whether id is registered.
func (a *Actions) Known(id string) bool {
	_, ok := a.index[id]
	return ok
}

// ActorKind is the kind of actor of an event.
type ActorKind string

const (
	ActorUser      ActorKind = "user"      // a signed-in person
	ActorCI        ActorKind = "ci"        // a CI identity without a user (publish session from a CI token)
	ActorCLI       ActorKind = "cli"       // the telesphorepo command line
	ActorSystem    ActorKind = "system"    // a job of a module
	ActorSSO       ActorKind = "sso"       // single sign-on, at a person's sign-in
	ActorAnonymous ActorKind = "anonymous" // no credential, or the tried name of a failed sign-in
)

// Actor is who did something.
type Actor struct {
	Kind   ActorKind
	UserID int64      // 0: none
	Name   string     // the username, the CI subject, "cli", the module ID of a job
	IP     netip.Addr // the client address; the zero value for none
}

type actorKey struct{}

// WithActor returns ctx with the actor a.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorOf returns the actor of ctx: anonymous when ctx has none.
func ActorOf(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a
	}
	return Actor{Kind: ActorAnonymous, Name: "anonymous"}
}

// System returns ctx with the job actor of module.
func System(ctx context.Context, module string) context.Context {
	return WithActor(ctx, Actor{Kind: ActorSystem, Name: module})
}

// CLI returns ctx with the command-line actor.
func CLI(ctx context.Context) context.Context {
	return WithActor(ctx, Actor{Kind: ActorCLI, Name: "cli"})
}

// Event is one audit row. Detail must never hold a secret value.
type Event struct {
	Action string
	RepoID int64  // 0: no repo
	Repo   string // the repo name, kept in the detail so it survives a repo delete
	Target string // a path, a prefix, a username, ...
	Detail map[string]any
}

// Entry is an audit row as the API shows it.
type Entry struct {
	ID          int64          `json:"id"`
	At          time.Time      `json:"at"`
	Actor       string         `json:"actor"`
	ActorUserID *int64         `json:"actor_user_id"`
	Action      string         `json:"action"`
	Repo        string         `json:"repo"`
	Target      string         `json:"target"`
	Detail      map[string]any `json:"detail"`
	IP          string         `json:"ip"`
}

// Log writes audit rows and deletes the old ones.
type Log struct {
	db      *db.DB
	rq, wq  *dbq.Queries
	actions *Actions
	log     *slog.Logger
}

// New returns the audit log. actions are the registered actions.
func New(d *db.DB, actions *Actions, log *slog.Logger) *Log {
	if log == nil {
		log = slog.Default()
	}
	return &Log{db: d, rq: dbq.New(d.R), wq: dbq.New(d.W), actions: actions, log: log}
}

// Actions returns the registered actions.
func (l *Log) Actions() []ActionDef { return l.actions.List() }

// clip shortens s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// row builds the row of e with the actor of ctx. An action that no module registered is an
// error.
func (l *Log) row(ctx context.Context, e Event) (dbq.InsertAuditEventParams, error) {
	if !l.actions.Known(e.Action) {
		err := fmt.Errorf("audit: action %q is not registered", e.Action)
		l.log.ErrorContext(ctx, "write audit event", "action", e.Action, "err", err.Error())
		return dbq.InsertAuditEventParams{}, err
	}
	a := ActorOf(ctx)
	detail := map[string]any{}
	for k, v := range e.Detail {
		detail[k] = v
	}
	if e.Repo != "" {
		detail["repo"] = e.Repo
	}
	js, err := json.Marshal(detail)
	if err != nil {
		js = []byte("{}")
	}
	ip := ""
	if a.IP.IsValid() {
		ip = a.IP.String()
	}
	return dbq.InsertAuditEventParams{
		At:          time.Now().Unix(),
		ActorUserID: sql.NullInt64{Int64: a.UserID, Valid: a.UserID != 0},
		Actor:       clip(a.Name, 200),
		Action:      e.Action,
		RepoID:      sql.NullInt64{Int64: e.RepoID, Valid: e.RepoID != 0},
		Target:      clip(e.Target, 1100),
		Detail:      string(js),
		Ip:          ip,
	}, nil
}

// Record writes e with the actor of ctx, detached from the cancellation of ctx. A failed write
// is logged and returned; the caller's action stands.
func (l *Log) Record(ctx context.Context, e Event) error {
	p, err := l.row(ctx, e)
	if err != nil {
		return err
	}
	if err := l.wq.InsertAuditEvent(context.WithoutCancel(ctx), p); err != nil {
		l.log.ErrorContext(ctx, "write audit event", "action", e.Action, "err", err.Error())
		return err
	}
	return nil
}

// RecordTx writes e with the actor of ctx in tx, the transaction of the change it records.
func (l *Log) RecordTx(ctx context.Context, tx *sql.Tx, e Event) error {
	p, err := l.row(ctx, e)
	if err != nil {
		return err
	}
	return l.wq.WithTx(tx).InsertAuditEvent(ctx, p)
}

// Filter narrows List. RepoID 0, ActorUserID 0, Action "" and a zero Since or Until match
// everything. ActorUserID is the person who acted (a CI subject or a failed sign-in name has
// none). Since and Until bound the time: the events at or after Since and before Until.
type Filter struct {
	Action      string
	RepoID      int64
	ActorUserID int64
	Since       time.Time
	Until       time.Time
}

// firstAt returns the id of the first event at or after t, or 0 when there is none.
func (l *Log) firstAt(ctx context.Context, t time.Time) (int64, error) {
	id, err := l.rq.FirstAuditAt(ctx, t.Unix())
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// List returns a newest-first keyset page: ids below after (0: first page). next is the cursor
// of the next page, 0 at the end. A time bound becomes an id bound: the id of the first event at
// or after the time (audit_events_at), so each filter keeps its keyset query.
func (l *Log) List(ctx context.Context, f Filter, after int64, limit int) ([]Entry, int64, error) {
	before := after
	if before <= 0 {
		before = math.MaxInt64
	}
	var since int64
	if !f.Since.IsZero() {
		id, err := l.firstAt(ctx, f.Since)
		if err != nil {
			return nil, 0, err
		}
		if id == 0 {
			return []Entry{}, 0, nil // nothing happened since then
		}
		since = id
	}
	if !f.Until.IsZero() {
		id, err := l.firstAt(ctx, f.Until)
		if err != nil {
			return nil, 0, err
		}
		if id != 0 {
			before = min(before, id)
		}
	}
	n := int64(limit)
	repo := sql.NullInt64{Int64: f.RepoID, Valid: f.RepoID != 0}
	actor := sql.NullInt64{Int64: f.ActorUserID, Valid: f.ActorUserID != 0}
	var rows []dbq.AuditEvent
	var err error
	switch {
	case f.ActorUserID != 0 && f.RepoID != 0 && f.Action != "":
		rows, err = l.rq.ListAuditByActorRepoAction(ctx, dbq.ListAuditByActorRepoActionParams{ActorUserID: actor, RepoID: repo, Action: f.Action, Before: before, Since: since, Limit: n})
	case f.ActorUserID != 0 && f.RepoID != 0:
		rows, err = l.rq.ListAuditByActorRepo(ctx, dbq.ListAuditByActorRepoParams{ActorUserID: actor, RepoID: repo, Before: before, Since: since, Limit: n})
	case f.ActorUserID != 0 && f.Action != "":
		rows, err = l.rq.ListAuditByActorAction(ctx, dbq.ListAuditByActorActionParams{ActorUserID: actor, Action: f.Action, Before: before, Since: since, Limit: n})
	case f.ActorUserID != 0:
		rows, err = l.rq.ListAuditByActor(ctx, dbq.ListAuditByActorParams{ActorUserID: actor, Before: before, Since: since, Limit: n})
	case f.RepoID != 0 && f.Action != "":
		rows, err = l.rq.ListAuditByRepoAction(ctx, dbq.ListAuditByRepoActionParams{RepoID: repo, Before: before, Since: since, Action: f.Action, Limit: n})
	case f.RepoID != 0:
		rows, err = l.rq.ListAuditByRepo(ctx, dbq.ListAuditByRepoParams{RepoID: repo, Before: before, Since: since, Limit: n})
	case f.Action != "":
		rows, err = l.rq.ListAuditByAction(ctx, dbq.ListAuditByActionParams{Action: f.Action, Before: before, Since: since, Limit: n})
	default:
		rows, err = l.rq.ListAudit(ctx, dbq.ListAuditParams{Before: before, Since: since, Limit: n})
	}
	if err != nil {
		return nil, 0, err
	}
	out := make([]Entry, 0, len(rows))
	for _, r := range rows {
		e := Entry{
			ID: r.ID, At: time.Unix(r.At, 0).UTC(), Actor: r.Actor, Action: r.Action,
			Target: r.Target, IP: r.Ip, Detail: map[string]any{},
		}
		if r.ActorUserID.Valid {
			id := r.ActorUserID.Int64
			e.ActorUserID = &id
		}
		if err := json.Unmarshal([]byte(r.Detail), &e.Detail); err != nil || e.Detail == nil {
			e.Detail = map[string]any{}
		}
		if name, ok := e.Detail["repo"].(string); ok {
			e.Repo = name
		}
		out = append(out, e)
	}
	var next int64
	if len(rows) > 0 && len(rows) == limit {
		next = rows[len(rows)-1].ID
	}
	return out, next, nil
}

// cleanupBatch is how many events one delete of Cleanup removes.
const cleanupBatch = 1000

// Cleanup deletes the events older than retention in batches.
func (l *Log) Cleanup(ctx context.Context, retention time.Duration) error {
	cut := time.Now().Add(-retention).Unix()
	for ctx.Err() == nil {
		n, err := l.wq.DeleteOldAudit(ctx, dbq.DeleteOldAuditParams{Before: cut, Limit: cleanupBatch})
		if err != nil {
			return err
		}
		if n < cleanupBatch {
			return nil
		}
	}
	return ctx.Err()
}
