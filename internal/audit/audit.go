// Package audit writes and lists the audit log: who did what, to which repo, from which
// address. The audit log is the only place that shows client IP addresses.
//
// Every action has a registered ID (ActionDef): each module registers the actions it records,
// and Record refuses an action that no module registered. An action also registers how its events
// read in Activity: a sentence and a Field for each detail key. The actor travels in the context: the
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
	"slices"
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
	RepoRename       = "repo.rename"        // detail renamed_from; the old name is a redirect
	RedirectAdd      = "repo.redirect.add"  // detail redirect
	RedirectRemove   = "repo.redirect.remove"
	SettingsUpdate   = "settings.update"
)

// ActionDef is one action of the audit log.
type ActionDef struct {
	ID        string `json:"id"`        // a dotted name, unique across modules, e.g. "ci.pipeline.create"
	Label     string `json:"label"`     // "CI pipeline created"
	Subsystem string `json:"subsystem"` // a registered permission subsystem, for grouping in the Activity filter
	// Sentence is how an event reads in Activity, with the placeholders {actor}, {repo}, {target}
	// and {<detail key>}: "{actor} uploaded {target} to {repo}". A placeholder names a value that
	// every event of the action has. A job's event reads without {actor} ("Backup finished").
	Sentence string `json:"sentence"`
	// Fields says how each detail key of the action reads: one for every key that the action
	// records, the keys of older events included. Add appends "repo" and "redirect_to", which Record
	// adds itself.
	Fields []Field `json:"fields"`
}

// FieldKind is how the value of a detail key reads.
type FieldKind string

const (
	KindText     FieldKind = "text"     // the value as it is
	KindCount    FieldKind = "count"    // a number with its unit: One, Many ("12,827 files")
	KindSize     FieldKind = "size"     // bytes ("14 GiB")
	KindBool     FieldKind = "bool"     // the phrase of true (Yes) or false (No); an empty phrase says nothing
	KindList     FieldKind = "list"     // strings; Values, when set, gives the words of each item
	KindEnum     FieldKind = "enum"     // the words of each value (Values); a value without words shows as is
	KindPerms    FieldKind = "perms"    // a permission set of Scope in words ("Download, publish", "Administrator")
	KindTime     FieldKind = "time"     // unix seconds or an RFC 3339 string
	KindDuration FieldKind = "duration" // seconds
	KindHidden   FieldKind = "hidden"   // never shown: ids, hashes
)

var fieldKinds = map[FieldKind]bool{KindText: true, KindCount: true, KindSize: true, KindBool: true, KindList: true,
	KindEnum: true, KindPerms: true, KindTime: true, KindDuration: true, KindHidden: true}

// Field is how one detail key of an action reads (GET /api/audit/actions). Build one with Text,
// Count, Size, Bool, List, Enum, Perms, Time, Duration or Hidden.
type Field struct {
	Key    string            `json:"key"`
	Label  string            `json:"label"` // the name of the fact in the event's details: "Files", "Folder"
	Kind   FieldKind         `json:"kind"`
	One    string            `json:"one,omitempty"`  // count: the unit of one ("file")
	Many   string            `json:"many,omitempty"` // count: the unit of more ("files")
	Yes    string            `json:"yes,omitempty"`  // bool: the phrase of true
	No     string            `json:"no,omitempty"`   // bool: the phrase of false
	Values map[string]string `json:"values,omitempty"`
	Scope  string            `json:"scope,omitempty"` // perms: "repo" or "server"
	// Quiet keeps the fact off the summary line of an event; its details still show it. For facts
	// nobody scans for: a folder path, a count that is mostly zero, an internal counter.
	Quiet bool `json:"quiet,omitempty"`

	words string // the word list that Actions.Fill puts in Values
}

// The field constructors.

func Text(key, label string) Field   { return Field{Key: key, Label: label, Kind: KindText} }
func Size(key, label string) Field   { return Field{Key: key, Label: label, Kind: KindSize} }
func List(key, label string) Field   { return Field{Key: key, Label: label, Kind: KindList} }
func Time(key, label string) Field   { return Field{Key: key, Label: label, Kind: KindTime} }
func Hidden(key, label string) Field { return Field{Key: key, Label: label, Kind: KindHidden} }

// Duration is a number of seconds.
func Duration(key, label string) Field { return Field{Key: key, Label: label, Kind: KindDuration} }

// Count is a number with its unit, singular and plural.
func Count(key, label, one, many string) Field {
	return Field{Key: key, Label: label, Kind: KindCount, One: one, Many: many}
}

// Bool says yes when the value is true and no when it is false; "" says nothing.
func Bool(key, label, yes, no string) Field {
	return Field{Key: key, Label: label, Kind: KindBool, Yes: yes, No: no}
}

// Enum gives the words of each value.
func Enum(key, label string, values map[string]string) Field {
	return Field{Key: key, Label: label, Kind: KindEnum, Values: values}
}

// Perms is a permission set of scope "repo" or "server".
func Perms(key, label, scope string) Field {
	return Field{Key: key, Label: label, Kind: KindPerms, Scope: scope}
}

// Quietly returns f off the summary line (Quiet).
func (f Field) Quietly() Field { f.Quiet = true; return f }

// Words returns f with the words of its values (a list's items, an enum's values).
func (f Field) Words(values map[string]string) Field { f.Values = values; return f }

// The word lists that the server fills once every module registered (Actions.Fill).
const (
	WordsSettings = "settings" // a setting key: its label
	WordsFormats  = "formats"  // a format name: its title
)

// From returns f with the words of the list words (WordsSettings, WordsFormats).
func (f Field) From(words string) Field { f.words = words; return f }

// The keys that Record adds to the detail of any event.
var envelope = []Field{
	Text("repo", "Repository"),
	Text("redirect_to", "Now named").Quietly(), // the request named the repository by an older name
}

var (
	actionRE      = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)+$`)
	placeholderRE = regexp.MustCompile(`\{([^{}]*)\}`)
)

// Actions is the registry of actions. It is filled while the modules register and then fixed.
type Actions struct {
	defs  []ActionDef
	keys  []map[string]bool // the field keys of each def
	index map[string]int
}

// NewActions returns an empty action registry.
func NewActions() *Actions { return &Actions{index: map[string]int{}} }

// Add registers defs. subsystem reports whether a permission subsystem is registered. It refuses
// a malformed or repeated ID, an empty label or sentence, an unknown subsystem, a field without a
// key, a label or a known kind, and a placeholder that names no field.
func (a *Actions) Add(defs []ActionDef, subsystem func(id string) bool) error {
	for _, d := range defs {
		if !actionRE.MatchString(d.ID) || d.Label == "" || d.Sentence == "" {
			return fmt.Errorf("audit: action %q needs a dotted ID, a label and a sentence", d.ID)
		}
		if _, taken := a.index[d.ID]; taken {
			return fmt.Errorf("audit: action %q is registered twice", d.ID)
		}
		if !subsystem(d.Subsystem) {
			return fmt.Errorf("audit: action %q: subsystem %q is not registered", d.ID, d.Subsystem)
		}
		keys := map[string]bool{}
		for _, f := range d.Fields {
			if err := f.check(); err != nil {
				return fmt.Errorf("audit: action %q: %w", d.ID, err)
			}
			if keys[f.Key] {
				return fmt.Errorf("audit: action %q: field %q is registered twice", d.ID, f.Key)
			}
			keys[f.Key] = true
		}
		d.Fields = slices.Clone(d.Fields)
		for _, f := range envelope {
			if !keys[f.Key] {
				keys[f.Key] = true
				d.Fields = append(d.Fields, f)
			}
		}
		for _, m := range placeholderRE.FindAllStringSubmatch(d.Sentence, -1) {
			if p := m[1]; p != "actor" && p != "target" && !keys[p] {
				return fmt.Errorf("audit: action %q: the sentence names {%s}, which is no field", d.ID, p)
			}
		}
		a.index[d.ID] = len(a.defs)
		a.defs = append(a.defs, d)
		a.keys = append(a.keys, keys)
	}
	return nil
}

func (f Field) check() error {
	switch {
	case f.Key == "" || f.Label == "" || !fieldKinds[f.Kind]:
		return fmt.Errorf("field %q needs a key, a label and a known kind (kind %q)", f.Key, f.Kind)
	case f.Kind == KindCount && (f.One == "" || f.Many == ""):
		return fmt.Errorf("count field %q needs its units", f.Key)
	case f.Kind == KindPerms && f.Scope != "repo" && f.Scope != "server":
		return fmt.Errorf("perms field %q needs the scope repo or server", f.Key)
	case f.Kind == KindEnum && len(f.Values) == 0 && f.words == "":
		return fmt.Errorf("enum field %q needs its words", f.Key)
	}
	return nil
}

// Fill puts values in every field whose words come from the list words (Field.From). The server
// calls it once every module registered, before it serves.
func (a *Actions) Fill(words string, values map[string]string) {
	for i := range a.defs {
		for j := range a.defs[i].Fields {
			if f := &a.defs[i].Fields[j]; f.words == words {
				f.Values = values
			}
		}
	}
}

// List returns every action in registration order.
func (a *Actions) List() []ActionDef { return a.defs }

// Known reports whether id is registered.
func (a *Actions) Known(id string) bool {
	_, ok := a.index[id]
	return ok
}

// unregistered returns the keys of detail that action registers no field for.
func (a *Actions) unregistered(action string, detail map[string]any) []string {
	keys := a.keys[a.index[action]]
	var out []string
	for k := range detail {
		if !keys[k] {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
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

type usedNameKey struct{}

// usedName is the name that a request used for a repo: a redirect of it.
type usedName struct {
	id   int64
	name string
}

// WithRepoName returns ctx with name, the redirect that a request used for repo id. An event of
// that repo then records name as its repo, and the repo's own name in detail.redirect_to, so
// Activity shows the name that the request used.
func WithRepoName(ctx context.Context, id int64, name string) context.Context {
	return context.WithValue(ctx, usedNameKey{}, usedName{id, name})
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
// error; a detail key that the action registers no field for is logged, and the event is written
// with it.
func (l *Log) row(ctx context.Context, e Event) (dbq.InsertAuditEventParams, error) {
	if !l.actions.Known(e.Action) {
		err := fmt.Errorf("audit: action %q is not registered", e.Action)
		l.log.ErrorContext(ctx, "write audit event", "action", e.Action, "err", err.Error())
		return dbq.InsertAuditEventParams{}, err
	}
	if keys := l.actions.unregistered(e.Action, e.Detail); len(keys) > 0 {
		l.log.ErrorContext(ctx, "audit: the action registers no field for these detail keys", "action", e.Action, "keys", keys)
	}
	a := ActorOf(ctx)
	detail := map[string]any{}
	for k, v := range e.Detail {
		detail[k] = v
	}
	if e.Repo != "" {
		detail["repo"] = e.Repo
		if u, ok := ctx.Value(usedNameKey{}).(usedName); ok && u.id == e.RepoID && u.name != e.Repo {
			detail["repo"], detail["redirect_to"] = u.name, e.Repo
		}
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
