// Package settings is the settings store: typed settings in the settings table (secrets
// AES-256-GCM encrypted), forced by TR_<KEY> environment overrides, falling back to defaults.
// Each module owns its groups of settings (Group): the definitions, the permission that edits
// them and the owner hooks. The effective values live in an immutable snapshot that is rebuilt
// on every write; request paths read the snapshot (Key.Get, View.Get), never the table.
package settings

import (
	"context"
	"crypto/cipher"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

var keyRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// Source says where a setting's effective value comes from.
type Source string

const (
	SourceStored  Source = "stored"
	SourceEnv     Source = "env"
	SourceDefault Source = "default"
)

// ValidationError reports invalid settings, keyed by setting key ("" for general errors).
type ValidationError struct {
	Message string
	Fields  map[string]string
}

func (e *ValidationError) Error() string {
	keys := slices.Sorted(maps.Keys(e.Fields))
	parts := make([]string, 0, len(keys)+1)
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	for _, k := range keys {
		if k == "" {
			parts = append(parts, e.Fields[k])
		} else {
			parts = append(parts, k+": "+e.Fields[k])
		}
	}
	return "invalid settings: " + strings.Join(parts, "; ")
}

func (e *ValidationError) add(key, msg string) {
	if _, ok := e.Fields[key]; !ok {
		e.Fields[key] = msg
	}
}

func (e *ValidationError) orNil() error {
	if len(e.Fields) == 0 && e.Message == "" {
		return nil
	}
	return e
}

func newVE() *ValidationError { return &ValidationError{Fields: map[string]string{}} }

// Snapshot is an immutable set of the effective settings. Read it with a Key or a View.
type Snapshot struct {
	reg     *registry
	values  []any // by Def index
	sources []Source
	stored  map[string]any    // decoded rows of the settings table
	broken  map[string]string // stored values that could not be read, with the reason
	dataDir string
	views   sync.Map // *View -> its value
}

// Source returns where the effective value of key comes from ("" for an unknown key).
func (s *Snapshot) Source(key string) Source {
	if d := s.reg.byKey[key]; d != nil {
		return s.sources[d.idx]
	}
	return ""
}

// DataDir returns the data directory of the server.
func (s *Snapshot) DataDir() string { return s.dataDir }

// Provider returns the current snapshot.
type Provider func() *Snapshot

// View is an owner's typed view of the settings, computed once per snapshot.
type View[T any] struct{ build func(*Snapshot) T }

// NewView returns a view that build computes.
func NewView[T any](build func(*Snapshot) T) *View[T] { return &View[T]{build: build} }

// Get returns the view of s.
func (v *View[T]) Get(s *Snapshot) T {
	if x, ok := s.views.Load(v); ok {
		return x.(T)
	}
	x, _ := s.views.LoadOrStore(v, v.build(s))
	return x.(T)
}

// Options configures Load.
type Options struct {
	// Dev makes the Dev value of every Def replace its Default. Used by "telesphorepo-server dev";
	// env still wins and the UI shows these as defaults, not as locked.
	Dev bool
	Log *slog.Logger
}

// Service owns the settings table and the effective snapshot.
type Service struct {
	db     *db.DB
	boot   *config.Boot
	reg    *registry
	aead   cipher.AEAD
	master []byte // the master key; DeriveKey derives keys from it
	log    *slog.Logger
	dev    bool
	env    map[string]string // non-empty TR_* overrides, captured at Load
	audit  *audit.Log        // records settings.update for each Patch that saves

	mu       sync.Mutex             // serializes writers
	degraded map[string]map[int]any // group ID -> the overrides it started with
	snap     atomic.Pointer[Snapshot]

	subsMu sync.Mutex
	subs   []func(old, new *Snapshot)
}

// Load reads the master key, the settings table and the environment overrides, builds the first
// snapshot and runs the Validate of every group (ModeStartup). An invalid environment value or a
// refusal of a Validate stops the server; a Validate that returns ErrDegraded starts it with its
// overrides. A stored value that is not valid is logged and ignored, so the server starts and the
// value can be fixed in the UI.
func Load(ctx context.Context, d *db.DB, boot *config.Boot, groups []*Group, opts Options) (*Service, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	reg, err := newRegistry(groups)
	if err != nil {
		return nil, err
	}
	master, err := loadMasterKey(boot, log)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(master)
	if err != nil {
		return nil, err
	}
	s := &Service{db: d, boot: boot, reg: reg, aead: aead, master: master, log: log, dev: opts.Dev,
		env: map[string]string{}, degraded: map[string]map[int]any{}}
	for _, def := range reg.defs {
		if v := os.Getenv(def.Env()); v != "" {
			s.env[def.Key] = v
		}
	}
	s.warnUnknownEnv()
	rows, broken, err := s.readDB(ctx)
	if err != nil {
		return nil, err
	}
	snap, ve := s.compose(rows, broken)
	if err := ve.orNil(); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	for _, g := range reg.groups {
		if g.Validate == nil {
			continue
		}
		dr := newDraft(snap, nil)
		err := g.Validate(dr, ModeStartup)
		if errors.Is(err, ErrDegraded) {
			log.Error("settings of group "+g.ID+" fail; the server starts without them until they are fixed", "error", err.Error())
			s.degraded[g.ID] = dr.overrides
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("settings: %s: %w", g.ID, err)
		}
		if dr.Failed() {
			return nil, fmt.Errorf("settings: %w", &ValidationError{Fields: dr.fields})
		}
	}
	s.snap.Store(snap)
	return s, nil
}

// bootEnv are TR_* variables read outside the settings (boot environment, tooling).
var bootEnv = map[string]bool{
	"TR_DATA_DIR": true, "TR_LISTEN": true, "TR_MASTER_KEY": true,
	"TR_BOOTSTRAP_ADMIN_USER": true, "TR_BOOTSTRAP_ADMIN_PASSWORD": true,
	"TR_ENGINE": true, // container engine selection (dev tooling, e2e)
}

// warnUnknownEnv logs TR_* variables that match no setting or boot variable (typos, removed
// settings), naming only the variable, never its value.
func (s *Service) warnUnknownEnv() {
	known := map[string]bool{}
	for _, def := range s.reg.defs {
		known[def.Env()] = true
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, "TR_") || bootEnv[name] || known[name] || strings.HasPrefix(name, "TR_E2E_") {
			continue
		}
		s.log.Warn("ignoring unknown environment variable; it matches no setting", "var", name)
	}
}

// SetAudit sets the audit log that Patch records settings.update in.
func (s *Service) SetAudit(a *audit.Log) { s.audit = a }

// Current returns the current snapshot.
func (s *Service) Current() *Snapshot { return s.snap.Load() }

// Groups returns every group, in module start order.
func (s *Service) Groups() []*Group { return s.reg.groups }

// Group returns the group id, or nil.
func (s *Service) Group(id string) *Group { return s.reg.byID[id] }

// Def returns the definition of key, or nil.
func (s *Service) Def(key string) *Def { return s.reg.byKey[key] }

// Subscribe registers fn to run after every applied change, with the previous and the new
// snapshot. fn runs synchronously in the writer; keep it short.
func (s *Service) Subscribe(fn func(old, new *Snapshot)) {
	s.subsMu.Lock()
	s.subs = append(s.subs, fn)
	s.subsMu.Unlock()
}

// readDB returns the decoded settings rows, skipping (and logging) unknown and invalid ones. A
// secret that does not decrypt is left out and named in broken.
func (s *Service) readDB(ctx context.Context) (map[string]any, map[string]string, error) {
	rows, err := dbq.New(s.db.R).ListSettings(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("read settings: %w", err)
	}
	out, broken := map[string]any{}, map[string]string{}
	for _, row := range rows {
		def := s.reg.byKey[row.Key]
		if def == nil {
			s.log.Warn("ignoring unknown stored setting", "key", row.Key)
			continue
		}
		raw := []byte(row.Value)
		if def.Secret {
			if raw, err = open(s.aead, row.Key, row.Value); err != nil {
				s.log.Warn("ignoring stored secret setting: "+err.Error()+"; set it again", "key", row.Key)
				broken[row.Key] = err.Error()
				continue
			}
		}
		v, err := def.decodeJSON(raw)
		if err != nil {
			s.log.Warn("ignoring invalid stored setting", "key", row.Key, "error", err.Error())
			continue
		}
		out[row.Key] = v
	}
	return out, broken, nil
}

func (s *Service) defaultText(def *Def) string {
	if s.dev && def.Dev != "" {
		return def.Dev
	}
	if def.DataDefault != "" {
		return config.JoinDataDir(s.boot.DataDir, def.DataDefault)
	}
	return def.Default
}

// compose builds a snapshot from stored rows, the environment and the defaults. An environment
// or default value that does not parse is a field error.
func (s *Service) compose(rows map[string]any, broken map[string]string) (*Snapshot, *ValidationError) {
	ve := newVE()
	n := len(s.reg.defs)
	snap := &Snapshot{reg: s.reg, values: make([]any, n), sources: make([]Source, n), stored: rows,
		broken: broken, dataDir: s.boot.DataDir}
	for i, def := range s.reg.defs {
		if text, ok := s.env[def.Key]; ok {
			v, err := def.parseText(text)
			if err != nil {
				ve.add(def.Key, def.Env()+": "+err.Error())
				continue
			}
			snap.values[i], snap.sources[i] = v, SourceEnv
			continue
		}
		if v, ok := rows[def.Key]; ok {
			snap.values[i], snap.sources[i] = v, SourceStored
			continue
		}
		v, err := def.parseText(s.defaultText(def))
		if err != nil {
			ve.add(def.Key, "invalid default: "+err.Error())
			continue
		}
		snap.values[i], snap.sources[i] = v, SourceDefault
	}
	return snap, ve
}

// withDegraded applies the overrides of the groups that started degraded, except skip.
func (s *Service) withDegraded(snap *Snapshot, skip map[string]bool) {
	for g, ov := range s.degraded {
		if skip[g] {
			continue
		}
		for i, v := range ov {
			snap.values[i] = v
		}
	}
}

// Item is one setting as shown by the API (GET /api/settings).
type Item struct {
	Key     string   `json:"key"`
	Env     string   `json:"env"`
	Group   string   `json:"group"`
	Label   string   `json:"label"`
	Help    string   `json:"help,omitempty"`
	Type    Type     `json:"type"`
	Options []string `json:"options,omitempty"`
	Secret  bool     `json:"secret"`
	// Value is the effective value; for secrets it is SecretValue (never the secret itself).
	Value   any `json:"value"`
	Default any `json:"default"` // nil for secrets
	// Display and DefaultDisplay put a size or a duration in words ("512 MiB", "90 days").
	Display        string `json:"display,omitempty"`
	DefaultDisplay string `json:"default_display,omitempty"`
	Source         Source `json:"source"`
	// Locked: forced by the environment variable Env, read-only in the UI.
	Locked bool `json:"locked"`
}

// SecretValue is what Items reports for a secret setting.
type SecretValue struct {
	Set bool `json:"set"`
}

// Items returns the settings of group g, in definition order.
func (s *Service) Items(g *Group) []Item {
	snap := s.Current()
	out := make([]Item, 0, len(g.Defs))
	for _, def := range g.Defs {
		it := Item{
			Key: def.Key, Env: def.Env(), Group: g.ID, Label: def.Label, Help: def.Help,
			Type: def.Type, Options: def.Options, Secret: def.Secret,
			Source: snap.sources[def.idx], Locked: snap.sources[def.idx] == SourceEnv,
		}
		if def.Secret {
			it.Value = SecretValue{Set: snap.values[def.idx] != ""}
		} else {
			it.Value = def.jsonValue(snap.values[def.idx])
			if dv, err := def.parseText(s.defaultText(def)); err == nil {
				it.Default = def.jsonValue(dv)
			}
		}
		it.Display, it.DefaultDisplay = display(snap.values[def.idx]), ""
		if def.Secret {
			it.Display = ""
		} else if dv, err := def.parseText(s.defaultText(def)); err == nil {
			it.DefaultDisplay = display(dv)
		}
		out = append(out, it)
	}
	return out
}

// display puts a size or a duration value in words; other values give "".
func display(v any) string {
	switch x := v.(type) {
	case int64:
		return formatSize(x)
	case time.Duration:
		return formatDuration(x)
	}
	return ""
}

// candidate applies patch (JSON values; null resets to the default) to the stored rows of snap.
// It returns the new rows and the keys whose stored row changes. An empty string for a secret
// resets it.
func (s *Service) candidate(snap *Snapshot, patch map[string]json.RawMessage) (map[string]any, []string, *ValidationError) {
	ve := newVE()
	rows := maps.Clone(snap.stored)
	var changed []string
	for key, raw := range patch {
		def := s.reg.byKey[key]
		if def == nil {
			ve.add(key, "unknown setting")
			continue
		}
		if string(raw) == "null" || len(raw) == 0 {
			if _, ok := rows[key]; ok {
				delete(rows, key)
				changed = append(changed, key)
			}
			continue
		}
		v, err := def.decodeJSON(raw)
		if err != nil {
			ve.add(key, err.Error())
			continue
		}
		if def.Secret && v == "" {
			if _, ok := rows[key]; ok {
				delete(rows, key)
				changed = append(changed, key)
			}
			continue
		}
		if snap.sources[def.idx] == SourceEnv {
			if !def.Secret && equal(v, snap.values[def.idx]) {
				continue // the UI echoing a locked value back is fine
			}
			ve.add(key, "set by environment variable "+def.Env()+"; change it there")
			continue
		}
		if old, ok := rows[key]; ok && equal(old, v) {
			continue
		}
		rows[key] = v
		changed = append(changed, key)
	}
	sort.Strings(changed)
	return rows, changed, ve
}

// touched returns the groups that patch names a key of, in group order.
func (s *Service) touched(patch map[string]json.RawMessage) []*Group {
	var out []*Group
	for _, g := range s.reg.groups {
		for _, d := range g.Defs {
			if _, ok := patch[d.Key]; ok {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

// draft builds the snapshot that patch would make and runs the Validate (ModeChange) of every
// group in groups on it.
func (s *Service) draft(patch map[string]json.RawMessage, groups []*Group) (*Draft, map[string]any, []string, error) {
	cur := s.Current()
	rows, changedKeys, ve := s.candidate(cur, patch)
	if err := ve.orNil(); err != nil {
		return nil, nil, nil, err
	}
	next, ve := s.compose(rows, cur.broken)
	if err := ve.orNil(); err != nil {
		return nil, nil, nil, err
	}
	skip := map[string]bool{}
	for _, g := range groups {
		skip[g.ID] = true
	}
	s.withDegraded(next, skip)
	d := newDraft(next, cur)
	for _, g := range groups {
		if g.Validate == nil {
			continue
		}
		if err := g.Validate(d, ModeChange); err != nil {
			return nil, nil, nil, &ValidationError{Message: err.Error(), Fields: d.fields}
		}
	}
	if d.Failed() {
		return nil, nil, nil, &ValidationError{Fields: d.fields}
	}
	return d, rows, changedKeys, nil
}

// Patch applies a partial update (setting key -> JSON value; null resets to the default). It
// builds the whole new state and runs the Validate of every group the patch touches
// (ModeChange), then their Apply hooks in group order: the first Apply that takes the change over
// holds its keys back and answers. It saves the other keys with one settings.update event that
// names them, and returns the Applied answer, or nil. A patch that saves no key records no event. Invalid input returns a *ValidationError, a refused Apply a *HookError. userID 0 records
// no author.
func (s *Service) Patch(ctx context.Context, patch map[string]json.RawMessage, userID int64) (*Applied, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := s.touched(patch)
	d, rows, changedKeys, err := s.draft(patch, groups)
	if err != nil {
		return nil, err
	}
	var applied *Applied
	for _, g := range groups {
		if g.Apply == nil {
			continue
		}
		ap, err := g.Apply(ctx, &ApplyCall{Prev: d.Prev, Next: d.Next, Patch: patch, UserID: userID})
		if err != nil {
			return nil, err
		}
		if ap != nil {
			applied = ap
			break
		}
	}
	if applied != nil {
		for _, k := range applied.Hold {
			if v, ok := d.Prev.stored[k]; ok {
				rows[k] = v
			} else {
				delete(rows, k)
			}
			changedKeys = slices.DeleteFunc(changedKeys, func(c string) bool { return c == k })
		}
	}
	for _, g := range groups {
		delete(s.degraded, g.ID)
	}
	if len(changedKeys) == 0 {
		return applied, nil
	}
	return applied, s.write(ctx, rows, changedKeys, userID, true)
}

// Save applies patch like Patch, without the Apply hooks. An owner calls it to finish a change
// that its Apply took over (e.g. the storage keys when a storage move is done).
func (s *Service) Save(ctx context.Context, patch map[string]json.RawMessage, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := s.touched(patch)
	_, rows, changedKeys, err := s.draft(patch, groups)
	if err != nil {
		return err
	}
	for _, g := range groups {
		delete(s.degraded, g.ID)
	}
	if len(changedKeys) == 0 {
		return nil
	}
	return s.write(ctx, rows, changedKeys, userID, false)
}

// Test merges patch over the current values, runs the Validate (ModeChange) of group g and
// then its Test. It saves nothing.
func (s *Service) Test(ctx context.Context, g *Group, patch map[string]json.RawMessage) error {
	s.mu.Lock()
	d, _, _, err := s.draft(patch, []*Group{g})
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if g.Test == nil {
		return nil
	}
	return g.Test(ctx, d)
}

// write persists the changed keys of rows, reloads the snapshot and notifies subscribers. With
// record, it writes one settings.update event that names the keys (never the values) in the same
// transaction. Caller holds s.mu.
func (s *Service) write(ctx context.Context, rows map[string]any, changedKeys []string, userID int64, record bool) error {
	var by sql.NullInt64
	if userID > 0 {
		by = sql.NullInt64{Int64: userID, Valid: true}
	}
	now := time.Now().Unix()
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(s.db.R).WithTx(tx)
		for _, key := range changedKeys {
			v, ok := rows[key]
			if !ok {
				if err := q.DeleteSetting(ctx, key); err != nil {
					return err
				}
				continue
			}
			def := s.reg.byKey[key]
			raw, err := json.Marshal(def.jsonValue(v))
			if err != nil {
				return err
			}
			value := string(raw)
			if def.Secret {
				value = seal(s.aead, key, raw)
			}
			if err := q.UpsertSetting(ctx, dbq.UpsertSettingParams{Key: key, Value: value, UpdatedAt: now, UpdatedBy: by}); err != nil {
				return err
			}
		}
		if !record || s.audit == nil {
			return nil
		}
		return s.audit.RecordTx(ctx, tx, audit.Event{Action: audit.SettingsUpdate, Target: strings.Join(changedKeys, ","),
			Detail: map[string]any{"keys": changedKeys}})
	})
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	rows, broken, err := s.readDB(ctx)
	if err != nil {
		return err
	}
	next, ve := s.compose(rows, broken)
	if err := ve.orNil(); err != nil {
		return err
	}
	s.withDegraded(next, nil)
	old := s.snap.Swap(next)
	s.log.Info("settings changed", "keys", strings.Join(changedKeys, ","))
	s.subsMu.Lock()
	subs := slices.Clone(s.subs)
	s.subsMu.Unlock()
	for _, fn := range subs {
		fn(old, next)
	}
	return nil
}
