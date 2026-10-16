// Package perm is the permission registry. Every permission belongs to a subsystem: a part of
// the server that a module owns, such as "repo" (the files and settings of one repository) or
// "nexus" (the Nexus migration). A module registers its subsystems and their permissions; each
// permission has a plain label and a one-line help text, so a person who gives a permission
// sees what it controls.
//
// The registry is fixed when the server starts. The database stores permissions as their IDs,
// and every write checks them against the registry.
package perm

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"regexp"
	"strings"
	"sync/atomic"
)

// ID names a permission: "<subsystem>.<name>", e.g. "repo.publish".
type ID string

// Scope says where a permission applies.
type Scope string

const (
	ScopeServer Scope = "server" // the whole server, e.g. people.manage
	ScopeRepo   Scope = "repo"   // one repository, given per repository
)

// The permissions that the kernel itself checks. The core module registers them.
const (
	RepositoriesAll ID = "repositories.all"
	PeopleManage    ID = "people.manage"
	SettingsGeneral ID = "settings.general"

	RepoDownload ID = "repo.download"
	RepoPublish  ID = "repo.publish"
	RepoDelete   ID = "repo.delete"
	RepoSettings ID = "repo.settings"
	RepoAccess   ID = "repo.access"
)

// Subsystem is a part of the server that permissions control.
type Subsystem struct {
	ID    string // e.g. "repo", "ci", "nexus"
	Scope Scope
	Label string // "Repository", "CI pipelines"
	Help  string // one line: what this part of the server is
}

// Def is one permission.
type Def struct {
	ID    ID
	Label string // plain words, e.g. "Publish"
	Help  string // one line: what a holder can do
	// Needs are permissions of the same scope that a holder must also hold.
	Needs []ID
	// SecondFactor makes the UI session of a holder need a second factor.
	SecondFactor bool
	// AllRepos (server scope only): the holder holds every repository permission on every repo.
	AllRepos bool
}

// maxPerms is the size of a Set.
const maxPerms = 256

var (
	subsystemRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	nameRE      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

// group is a registered subsystem with its module and permissions.
type group struct {
	Subsystem
	module string
	perms  []int // indexes into Registry.defs
}

// Registry holds every subsystem and permission. It is filled while the modules register and
// then fixed.
type Registry struct {
	groups []*group
	defs   []Def
	sub    []int // defs index -> groups index
	index  map[ID]int
	fixed  bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{index: map[ID]int{}} }

// Add registers subsystem s of module and its permissions, in order. It refuses an ID that is
// taken or malformed, a permission whose ID does not start with "<subsystem>.", a need that is
// not registered yet or has another scope, and AllRepos on a repository permission.
func (g *Registry) Add(module string, s Subsystem, defs ...Def) error {
	if g.fixed {
		return errors.New("perm: the registry is fixed")
	}
	if !subsystemRE.MatchString(s.ID) || (s.Scope != ScopeServer && s.Scope != ScopeRepo) || s.Label == "" {
		return fmt.Errorf("perm: subsystem %q needs an ID, a scope and a label", s.ID)
	}
	for _, x := range g.groups {
		if x.ID == s.ID {
			return fmt.Errorf("perm: subsystem %q is registered by module %s", s.ID, x.module)
		}
	}
	grp := &group{Subsystem: s, module: module}
	gi := len(g.groups)
	for _, d := range defs {
		name, ok := strings.CutPrefix(string(d.ID), s.ID+".")
		if !ok || !nameRE.MatchString(name) {
			return fmt.Errorf("perm: %q must be %s.<name>", d.ID, s.ID)
		}
		if _, taken := g.index[d.ID]; taken {
			return fmt.Errorf("perm: %q is registered twice", d.ID)
		}
		if d.Label == "" || d.Help == "" {
			return fmt.Errorf("perm: %q needs a label and a help text", d.ID)
		}
		if d.AllRepos && s.Scope != ScopeServer {
			return fmt.Errorf("perm: %q: only a server permission gives all repositories", d.ID)
		}
		for _, n := range d.Needs {
			i, ok := g.index[n]
			if !ok || (g.sub[i] < len(g.groups) && g.groups[g.sub[i]].Scope != s.Scope) {
				return fmt.Errorf("perm: %q needs %q, which is not an earlier permission of scope %s", d.ID, n, s.Scope)
			}
		}
		if len(g.defs) == maxPerms {
			return fmt.Errorf("perm: more than %d permissions", maxPerms)
		}
		g.index[d.ID] = len(g.defs)
		g.defs = append(g.defs, d)
		g.sub = append(g.sub, gi)
		grp.perms = append(grp.perms, len(g.defs)-1)
	}
	g.groups = append(g.groups, grp)
	return nil
}

// Fix ends the registration and makes g the registry that every Set reads.
func (g *Registry) Fix() {
	g.fixed = true
	active.Store(g)
}

var active atomic.Pointer[Registry]

// Current returns the registry of the server. It panics before the server built it.
func Current() *Registry {
	g := active.Load()
	if g == nil {
		panic("perm: no registry; the modules have not registered")
	}
	return g
}

// HasSubsystem reports whether a module registered the subsystem id.
func (g *Registry) HasSubsystem(id string) bool {
	for _, x := range g.groups {
		if x.ID == id {
			return true
		}
	}
	return false
}

// Def returns the permission id, or nil when no module registers it.
func (g *Registry) Def(id ID) *Def {
	i, ok := g.index[id]
	if !ok {
		return nil
	}
	return &g.defs[i]
}

// ScopeOf returns the scope of id ("" when no module registers it).
func (g *Registry) ScopeOf(id ID) Scope {
	i, ok := g.index[id]
	if !ok {
		return ""
	}
	return g.groups[g.sub[i]].Scope
}

// Label returns the label of id, or id itself when no module registers it.
func (g *Registry) Label(id ID) string {
	if d := g.Def(id); d != nil {
		return d.Label
	}
	return string(id)
}

// All returns every permission of scope sc.
func (g *Registry) All(sc Scope) Set {
	var s Set
	for i := range g.defs {
		if g.groups[g.sub[i]].Scope == sc {
			s.set(i)
		}
	}
	return s
}

// AllServer returns every server permission: an administrator.
func (g *Registry) AllServer() Set { return g.All(ScopeServer) }

// AllRepo returns every repository permission.
func (g *Registry) AllRepo() Set { return g.All(ScopeRepo) }

// ErrUnknown is an ID that no module registers, or of the wrong scope.
var ErrUnknown = errors.New("unknown permission")

// Parse reads ids of scope sc into a set and checks the needs. An unknown ID or one of another
// scope returns an error that wraps ErrUnknown; a broken need returns a *NeedsError.
func (g *Registry) Parse(sc Scope, ids []string) (Set, error) {
	var s Set
	for _, x := range ids {
		i, ok := g.index[ID(x)]
		if !ok || g.groups[g.sub[i]].Scope != sc {
			return Set{}, fmt.Errorf("%w: %q is not a %s permission", ErrUnknown, x, sc)
		}
		s.set(i)
	}
	return s, g.CheckNeeds(s)
}

// NeedsError is a set that holds a permission without one of the permissions it needs.
type NeedsError struct {
	ID, Need ID
	label    string
	needs    string
}

func (e *NeedsError) Error() string {
	return fmt.Sprintf("%s needs %s (%s needs %s)", e.label, e.needs, e.ID, e.Need)
}

// CheckNeeds returns a *NeedsError when s holds a permission without one it needs.
func (g *Registry) CheckNeeds(s Set) error {
	for _, i := range s.indexes() {
		for _, n := range g.defs[i].Needs {
			if !s.Has(n) {
				return &NeedsError{ID: g.defs[i].ID, Need: n, label: g.defs[i].Label, needs: g.Label(n)}
			}
		}
	}
	return nil
}

// SecondFactor reports whether a holder of s needs a second factor for a UI session.
func (g *Registry) SecondFactor(s Set) bool {
	for _, i := range s.indexes() {
		if g.defs[i].SecondFactor {
			return true
		}
	}
	return false
}

// AllRepos reports whether s holds a server permission that gives every repository permission.
func (g *Registry) AllRepos(s Set) bool {
	for _, i := range s.indexes() {
		if g.defs[i].AllRepos {
			return true
		}
	}
	return false
}

// Managing returns the permissions of scope sc that need a second factor: the ones that manage
// rather than use, e.g. repo.settings or repo.access.
func (g *Registry) Managing(sc Scope) []ID {
	var out []ID
	for i, d := range g.defs {
		if d.SecondFactor && g.groups[g.sub[i]].Scope == sc {
			out = append(out, d.ID)
		}
	}
	return out
}

// AllReposIDs returns the server permissions that give every repository permission.
func (g *Registry) AllReposIDs() []ID {
	var out []ID
	for _, d := range g.defs {
		if d.AllRepos {
			out = append(out, d.ID)
		}
	}
	return out
}

// Dependents returns id and every permission that needs it, directly or through another one.
func (g *Registry) Dependents(id ID) Set {
	s := SetOf(id)
	for changed := true; changed; {
		changed = false
		for i, d := range g.defs {
			if s.hasIndex(i) {
				continue
			}
			for _, n := range d.Needs {
				if s.Has(n) {
					s.set(i)
					changed = true
					break
				}
			}
		}
	}
	return s
}

// SubsystemJSON is a subsystem as GET /api/permissions shows it.
type SubsystemJSON struct {
	ID          string    `json:"id"`
	Scope       Scope     `json:"scope"`
	Label       string    `json:"label"`
	Help        string    `json:"help"`
	Module      string    `json:"module"`
	Permissions []DefJSON `json:"permissions"`
}

// DefJSON is a permission as GET /api/permissions shows it.
type DefJSON struct {
	ID           ID     `json:"id"`
	Label        string `json:"label"`
	Help         string `json:"help"`
	Needs        []ID   `json:"needs"`
	SecondFactor bool   `json:"second_factor"`
	AllRepos     bool   `json:"all_repos"`
}

// Subsystems returns every subsystem with its permissions, in module start order and then in
// registration order.
func (g *Registry) Subsystems() []SubsystemJSON {
	out := make([]SubsystemJSON, 0, len(g.groups))
	for _, grp := range g.groups {
		sj := SubsystemJSON{ID: grp.ID, Scope: grp.Scope, Label: grp.Label, Help: grp.Help, Module: grp.module, Permissions: []DefJSON{}}
		for _, i := range grp.perms {
			d := g.defs[i]
			needs := d.Needs
			if needs == nil {
				needs = []ID{}
			}
			sj.Permissions = append(sj.Permissions, DefJSON{ID: d.ID, Label: d.Label, Help: d.Help, Needs: needs, SecondFactor: d.SecondFactor, AllRepos: d.AllRepos})
		}
		out = append(out, sj)
	}
	return out
}

// Set is a set of permissions: a bit mask over the registry index. The zero Set is empty.
type Set struct{ bits [maxPerms / 64]uint64 }

func (s *Set) set(i int)          { s.bits[i/64] |= 1 << (i % 64) }
func (s Set) hasIndex(i int) bool { return s.bits[i/64]&(1<<(i%64)) != 0 }
func (s Set) indexes() []int {
	var out []int
	for w, b := range s.bits {
		for b != 0 {
			i := bits.TrailingZeros64(b)
			out = append(out, w*64+i)
			b &^= 1 << i
		}
	}
	return out
}

// SetOf returns the set of ids. An ID that no module registers is left out.
func SetOf(ids ...ID) Set {
	g := Current()
	var s Set
	for _, id := range ids {
		if i, ok := g.index[id]; ok {
			s.set(i)
		}
	}
	return s
}

// SetOfStrings is SetOf for stored text, e.g. the rows of user_permissions. A stored ID that no
// module registers is left out.
func SetOfStrings(ids []string) Set {
	g := Current()
	var s Set
	for _, id := range ids {
		if i, ok := g.index[ID(id)]; ok {
			s.set(i)
		}
	}
	return s
}

// Joined reads a comma-joined column of IDs ("" for none).
func Joined(col string) Set {
	if col == "" {
		return Set{}
	}
	return SetOfStrings(strings.Split(col, ","))
}

// Has reports whether s holds id.
func (s Set) Has(id ID) bool {
	i, ok := Current().index[id]
	return ok && s.hasIndex(i)
}

// HasAny reports whether s holds one of ids.
func (s Set) HasAny(ids ...ID) bool {
	for _, id := range ids {
		if s.Has(id) {
			return true
		}
	}
	return false
}

// Contains reports whether s holds every permission of t.
func (s Set) Contains(t Set) bool { return t.Minus(s).Empty() }

// Empty reports whether s holds nothing.
func (s Set) Empty() bool { return s == Set{} }

// Union returns the permissions of s or t.
func (s Set) Union(t Set) Set {
	for i := range s.bits {
		s.bits[i] |= t.bits[i]
	}
	return s
}

// Intersect returns the permissions of s and t.
func (s Set) Intersect(t Set) Set {
	for i := range s.bits {
		s.bits[i] &= t.bits[i]
	}
	return s
}

// Minus returns the permissions of s that t does not hold.
func (s Set) Minus(t Set) Set {
	for i := range s.bits {
		s.bits[i] &^= t.bits[i]
	}
	return s
}

// Of returns the permissions of s of scope sc.
func (s Set) Of(sc Scope) Set { return s.Intersect(Current().All(sc)) }

// IDs returns the IDs of s in registry order.
func (s Set) IDs() []ID {
	g := Current()
	idx := s.indexes()
	out := make([]ID, len(idx))
	for j, i := range idx {
		out[j] = g.defs[i].ID
	}
	return out
}

// Strings returns the IDs of s as text, in registry order.
func (s Set) Strings() []string {
	ids := s.IDs()
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

// Labels returns the labels of s, in registry order.
func (s Set) Labels() []string {
	g := Current()
	idx := s.indexes()
	out := make([]string, len(idx))
	for j, i := range idx {
		out[j] = g.defs[i].Label
	}
	return out
}

// MarshalJSON writes the IDs of s.
func (s Set) MarshalJSON() ([]byte, error) { return json.Marshal(s.Strings()) }
