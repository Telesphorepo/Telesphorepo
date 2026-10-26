package settings

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/perm"
)

// Type is a setting's value type. The value a Key returns: string -> string, bool -> bool,
// int -> int64, size -> int64 (bytes), duration -> time.Duration, list -> []string,
// map -> map[string][]string, image -> []byte.
// The JSON form of a duration is its text ("15m"). Input of a size or a duration accepts
// friendly forms ("512MiB", "1.5 GiB", "90 days"). A map is a JSON object of string to a list of
// strings, in the environment too. An image is the bytes of an image file, in standard base64 in
// its JSON form and in the environment; GET /api/settings reports only whether it is set.
type Type string

const (
	TypeString   Type = "string"
	TypeBool     Type = "bool"
	TypeInt      Type = "int"
	TypeSize     Type = "size"
	TypeDuration Type = "duration"
	TypeList     Type = "list"
	TypeMap      Type = "map"
	TypeImage    Type = "image"
)

// Def describes one setting. Its environment override is TR_<KEY upper-cased, dots -> '_'>.
type Def struct {
	Key   string
	Label string
	Help  string
	Type  Type
	// Default is the text form of the default, parsed like an environment value ("" = empty).
	Default string
	// Dev replaces Default under "telesphorepo-server dev" ("" = same as Default).
	Dev string
	// DataDefault, when set, makes the default the path DataDefault in the data directory.
	DataDefault string
	Secret      bool
	// Zero lets a duration or a whole number be 0 (e.g. edge.access_ttl, where 0 means no cache).
	Zero    bool
	Options []string // allowed values (lower case) of an enumerated string
	// Normalize puts a decoded value in its canonical form (optional).
	Normalize func(v any) any
	// Validate checks a decoded, normalized value (optional).
	Validate func(v any) error

	idx   int    // index in the registry, set by Load
	group *Group // set by Load
}

// Env is the environment variable that overrides this setting.
func (d *Def) Env() string {
	return "TR_" + strings.ToUpper(strings.ReplaceAll(d.Key, ".", "_"))
}

// Group returns the group of d (nil before Load).
func (d *Def) Group() *Group { return d.group }

// Key is a typed handle of a setting. The owner of a setting declares it next to its Def.
type Key[T any] struct{ def *Def }

// Def returns the definition of k.
func (k Key[T]) Def() *Def { return k.def }

// Name returns the setting key, e.g. "base_url".
func (k Key[T]) Name() string { return k.def.Key }

// Get returns the effective value of k in s.
func (k Key[T]) Get(s *Snapshot) T {
	v, _ := s.values[k.def.idx].(T)
	return v
}

// String declares a string setting.
func String(d *Def) Key[string] { d.Type = TypeString; return Key[string]{d} }

// Bool declares a bool setting.
func Bool(d *Def) Key[bool] { d.Type = TypeBool; return Key[bool]{d} }

// Int declares a whole number setting.
func Int(d *Def) Key[int64] { d.Type = TypeInt; return Key[int64]{d} }

// Size declares a size setting, in bytes.
func Size(d *Def) Key[int64] { d.Type = TypeSize; return Key[int64]{d} }

// Duration declares a duration setting.
func Duration(d *Def) Key[time.Duration] { d.Type = TypeDuration; return Key[time.Duration]{d} }

// List declares a list setting.
func List(d *Def) Key[[]string] { d.Type = TypeList; return Key[[]string]{d} }

// Map declares a map setting: string keys to lists of strings.
func Map(d *Def) Key[map[string][]string] { d.Type = TypeMap; return Key[map[string][]string]{d} }

// Image declares an image setting: the bytes of an image file, which its Validate checks.
func Image(d *Def) Key[[]byte] { d.Type = TypeImage; return Key[[]byte]{d} }

// Mode is when a group's Validate runs.
type Mode int

const (
	// ModeStartup checks the stored rows and the environment when the server starts.
	ModeStartup Mode = iota
	// ModeChange checks a PATCH or a test of the settings.
	ModeChange
)

// ErrDegraded is the error a group's Validate wraps at startup when the server starts with
// overrides (Draft.Override) instead of the values that failed. The loader logs it and keeps the
// overrides until a PATCH of the group passes Validate. In ModeChange it is a 400 like any error.
var ErrDegraded = errors.New("settings: degraded")

// Group is a group of settings that a module owns: the permission that reads and edits them,
// the UI page that edits them, and the owner hooks.
type Group struct {
	ID    string // e.g. "general", "storage", "sso"
	Label string
	// Perm is the server permission that reads and edits every setting of the group.
	Perm perm.ID
	// Page is the UI path that edits the group; "" shows it on Settings > General.
	Page string
	Defs []*Def
	// Module is the ID of the owner module (set when the module registers the group).
	Module string

	// Validate checks the group as a whole: cross-field rules, field errors through d.Fail.
	Validate func(d *Draft, mode Mode) error
	// Test checks the values against the outside world, e.g. a probe of a blob store
	// (POST /api/settings/{group}/test). It runs after Validate and saves nothing.
	Test func(ctx context.Context, d *Draft) error
	// Apply runs before a PATCH of the group saves. It may take the change over (Applied).
	Apply func(ctx context.Context, c *ApplyCall) (*Applied, error)

	checks []func(d *Draft) // AddCheck
}

// AddCheck adds a check of a change of group id that needs services outside the owner module
// (e.g. that a repo named by a setting exists). It runs after the group's Validate on every PATCH
// and test of the group (ModeChange), never at startup, and records field errors with d.Fail.
// Call it before the server serves.
func (s *Service) AddCheck(id string, fn func(d *Draft)) {
	g := s.reg.byID[id]
	if g == nil {
		panic("settings: AddCheck of unknown group " + id)
	}
	g.checks = append(g.checks, fn)
}

// Draft is a set of values that a group's Validate or Test checks.
type Draft struct {
	// Next are the values that the change or the startup would make effective.
	Next *Snapshot
	// Prev are the values before the change; nil at startup.
	Prev *Snapshot

	fields    map[string]string
	overrides map[int]any
}

func newDraft(next, prev *Snapshot) *Draft {
	return &Draft{Next: next, Prev: prev, fields: map[string]string{}, overrides: map[int]any{}}
}

// Fail records a field error of key.
func (d *Draft) Fail(key, msg string) {
	if _, ok := d.fields[key]; !ok {
		d.fields[key] = msg
	}
}

// Failed reports whether a field error was recorded.
func (d *Draft) Failed() bool { return len(d.fields) > 0 }

// Source returns where the value of key in Next comes from.
func (d *Draft) Source(key string) Source { return d.Next.Source(key) }

// Broken returns why the stored value of key could not be read (a secret that does not
// decrypt), or "". Next holds the default of such a key.
func (d *Draft) Broken(key string) string { return d.Next.broken[key] }

// Override sets the effective value of key in Next, in the form its Key returns, and saves
// nothing. A Validate that overrides returns an error that wraps ErrDegraded. It drops the views
// of Next, so a View built before the override gives the overridden value.
func (d *Draft) Override(key string, v any) {
	def := d.Next.reg.byKey[key]
	if def == nil {
		panic("settings: override of unknown key " + key)
	}
	d.Next.values[def.idx] = v
	d.Next.views.Clear()
	d.overrides[def.idx] = v
}

// Changed reports whether one of keys has another value in Next than in Prev. At startup
// nothing changes.
func (d *Draft) Changed(keys ...string) bool { return changed(d.Prev, d.Next, keys) }

func changed(prev, next *Snapshot, keys []string) bool {
	if prev == nil {
		return false
	}
	for _, k := range keys {
		def := next.reg.byKey[k]
		if def != nil && !equal(prev.values[def.idx], next.values[def.idx]) {
			return true
		}
	}
	return false
}

// ApplyCall is a PATCH that an owner's Apply looks at before it is saved.
type ApplyCall struct {
	Prev, Next *Snapshot
	Patch      map[string]json.RawMessage
	UserID     int64
}

// Changed reports whether one of keys has another value in Next than in Prev.
func (c *ApplyCall) Changed(keys ...string) bool { return changed(c.Prev, c.Next, keys) }

// Applied lets an owner take over a change: Hold keys are not saved now; Status and Body answer
// the PATCH (e.g. 202 {"move": Move}).
type Applied struct {
	Hold   []string
	Status int
	Body   any
}

// HookError is a refusal of an Apply or Test hook with its HTTP status and the message per
// setting (Fields may be nil).
type HookError struct {
	Status  int
	Message string
	Fields  map[string]string
}

func (e *HookError) Error() string { return e.Message }

// registry holds the groups and definitions that Load got, in order.
type registry struct {
	groups []*Group
	defs   []*Def
	byKey  map[string]*Def
	byID   map[string]*Group
}

var groupRE = keyRE

// newRegistry checks the groups and numbers their definitions.
func newRegistry(groups []*Group) (*registry, error) {
	r := &registry{byKey: map[string]*Def{}, byID: map[string]*Group{}}
	for _, g := range groups {
		if !groupRE.MatchString(g.ID) || g.Label == "" || g.Perm == "" {
			return nil, fmt.Errorf("settings: group %q needs an ID, a label and a permission", g.ID)
		}
		if r.byID[g.ID] != nil {
			return nil, fmt.Errorf("settings: group %q is registered twice", g.ID)
		}
		if perm.Current().ScopeOf(g.Perm) != perm.ScopeServer {
			return nil, fmt.Errorf("settings: group %q: %q is not a server permission", g.ID, g.Perm)
		}
		r.byID[g.ID] = g
		r.groups = append(r.groups, g)
		for _, d := range g.Defs {
			if !keyRE.MatchString(d.Key) || d.Type == "" {
				return nil, fmt.Errorf("settings: key %q needs a name and a type", d.Key)
			}
			if r.byKey[d.Key] != nil {
				return nil, fmt.Errorf("settings: key %q is registered twice", d.Key)
			}
			d.idx, d.group = len(r.defs), g
			r.defs = append(r.defs, d)
			r.byKey[d.Key] = d
		}
	}
	return r, nil
}

// parseText parses an environment or default value.
func (d *Def) parseText(s string) (any, error) {
	var v any
	switch d.Type {
	case TypeString:
		v = s
	case TypeBool:
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return nil, errors.New("must be true or false")
		}
		v = b
	case TypeInt:
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, errors.New("must be a whole number")
		}
		v = n
	case TypeSize:
		n, err := parseSize(s)
		if err != nil {
			return nil, err
		}
		v = n
	case TypeDuration:
		dur, err := parseDuration(s)
		if err != nil {
			return nil, errors.New(durationHint)
		}
		v = dur
	case TypeList:
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		v = out
	case TypeMap:
		if strings.TrimSpace(s) == "" {
			return d.check(map[string][]string(nil))
		}
		return d.decodeJSON(json.RawMessage(s))
	case TypeImage:
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
		if err != nil {
			return nil, errors.New("must be standard base64")
		}
		v = b
	}
	return d.check(v)
}

const durationHint = `must be a positive duration such as "15m", "12h", "90 days" or "1h30m"`

// decodeJSON parses a value from the API or the database.
func (d *Def) decodeJSON(raw json.RawMessage) (any, error) {
	var v any
	switch d.Type {
	case TypeString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, errors.New("must be a string")
		}
		v = s
	case TypeDuration:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, errors.New("must be a string")
		}
		return d.parseText(s)
	case TypeBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return nil, errors.New("must be true or false")
			}
			return d.parseText(s)
		}
		v = b
	case TypeInt:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return nil, errors.New("must be a whole number")
			}
			return d.parseText(s)
		}
		v = n
	case TypeSize:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return nil, errors.New(sizeHint)
			}
			return d.parseText(s)
		}
		v = n
	case TypeList:
		var l []string
		if err := json.Unmarshal(raw, &l); err != nil {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return nil, errors.New("must be a list of strings")
			}
			return d.parseText(s)
		}
		var out []string
		for _, p := range l {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		v = out
	case TypeMap:
		var m map[string][]string
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, errors.New("must be an object of lists of strings")
		}
		if len(m) == 0 {
			m = nil
		}
		v = m
	case TypeImage:
		var b []byte
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, errors.New("must be a string of standard base64")
		}
		v = b
	}
	return d.check(v)
}

// check normalizes and validates a decoded value.
func (d *Def) check(v any) (any, error) {
	if d.Normalize != nil {
		v = d.Normalize(v)
	}
	switch d.Type {
	case TypeInt:
		if n := v.(int64); n < 0 || (n == 0 && !d.Zero) {
			return nil, errors.New("must be a positive whole number")
		}
	case TypeSize:
		if v.(int64) <= 0 {
			return nil, errors.New("must be a positive size")
		}
	case TypeDuration:
		if dur := v.(time.Duration); dur < 0 || (dur == 0 && !d.Zero) {
			return nil, errors.New(durationHint)
		}
	case TypeList:
		if l := v.([]string); len(l) == 0 {
			v = []string(nil)
		}
	case TypeImage:
		if b := v.([]byte); len(b) == 0 {
			return []byte(nil), nil // no image: nothing to validate
		}
	}
	if len(d.Options) > 0 {
		s, _ := v.(string)
		if !slices.Contains(d.Options, s) {
			return nil, fmt.Errorf("must be one of %s", strings.Join(d.Options, ", "))
		}
	}
	if d.Validate != nil {
		if err := d.Validate(v); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// jsonValue is the JSON form of a value: a duration as its text, an empty list as [], an empty
// map as {}.
func (d *Def) jsonValue(v any) any {
	switch x := v.(type) {
	case time.Duration:
		return canonicalDuration(x)
	case []string:
		if x == nil {
			return []string{}
		}
	case map[string][]string:
		if x == nil {
			return map[string][]string{}
		}
	}
	return v
}

func equal(a, b any) bool {
	switch x := a.(type) {
	case []string:
		y, _ := b.([]string)
		return slices.Equal(x, y)
	case map[string][]string:
		y, _ := b.(map[string][]string)
		return maps.EqualFunc(x, y, slices.Equal[[]string])
	case []byte:
		y, _ := b.([]byte)
		return bytes.Equal(x, y)
	}
	return a == b
}
