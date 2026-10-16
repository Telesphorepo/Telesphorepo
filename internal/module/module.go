// Package module is the contract of an installable unit of the server. Every repository format
// and every core feature with routes is a module. A module declares what it adds in Register,
// like Flarum's extend.php: each Extender call adds one entry to the module's manifest, and the
// server reads the manifests. The server never finds a capability by type assertion.
package module

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"net/http"
	"regexp"
	"slices"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/clients"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/token"
)

// Module is one installable unit: a repository format, or a core feature with routes.
type Module struct {
	// ID is the name of the module: ^[a-z][a-z0-9-]{0,31}$. It is also the schema component, the
	// prefix of its job names and the tag of its log.
	ID    string
	Title string // display name, e.g. "Maven / Gradle", "Nexus migration"
	// Requires are the IDs of the modules this module uses (Use). They come earlier in the list,
	// so they start first and stop last.
	Requires []string
	// Register declares what the module adds. It runs once per process, before the database
	// opens, and calls only Extender methods.
	Register func(x *Extender)
}

var idRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// reserved IDs collide with paths of the server.
var reserved = map[string]bool{"api": true, "healthz": true, "assets": true, "setup": true}

// Check panics on a module list that breaks the contract: a bad, reserved or repeated ID, or a
// Requires entry that is not earlier in the list.
func Check(mods []Module) {
	seen := map[string]bool{}
	for _, m := range mods {
		if !idRE.MatchString(m.ID) || reserved[m.ID] || seen[m.ID] {
			panic(fmt.Sprintf("module: invalid, reserved or repeated ID %q", m.ID))
		}
		for _, r := range m.Requires {
			if !seen[r] {
				panic(fmt.Sprintf("module %s: requires %q, which is not an earlier module", m.ID, r))
			}
		}
		if m.Register == nil {
			panic(fmt.Sprintf("module %s: no Register", m.ID))
		}
		seen[m.ID] = true
	}
}

// Manifest is what one module declared in Register.
type Manifest struct {
	Module        Module
	Schema        *db.Component
	Settings      []*settings.Group
	Permissions   []Permissions
	AuditActions  []audit.ActionDef
	OpTypes       []ops.Type
	Inits         []func(ctx context.Context, h *Host) error
	Routes        []func(r *route.Router)
	RootPrefixes  []string
	Jobs          []jobs.Job
	Formats       []format.Spec
	RepoFields    []repos.Field
	Guards        []access.Guard
	CIProviders   []ci.Provider
	Clients       []clients.Client
	ResolveClient func(r *http.Request) httpx.Client
	TokenKinds    []TokenKind
	Demo          *DemoData
	Seed          SeedFunc
}

// Permissions is a subsystem of the module and its permissions.
type Permissions struct {
	Subsystem perm.Subsystem
	Defs      []perm.Def
}

// TokenKind is a token prefix and the verifier of its tokens.
type TokenKind struct {
	Prefix token.Prefix
	Verify auth.Verifier
}

// DemoData fills the module's part of the demo dataset of "telesphorepo-server dev". Repos lists the
// repos it fills, all of the module's format; the command creates them, gives users access, and
// calls Fill once for each repo in that order, with a generator seeded by the repo's place in
// the dataset. Fill stores the files through run (run.Add, or the module's own upload pipeline
// and run.Stored) and announces how many it will store with run.Expect.
type DemoData struct {
	Repos []DemoRepo
	Fill  func(ctx context.Context, run *demo.Run, repo *repos.Repo, rng *rand.Rand) error
}

// DemoRepo is a repo of the demo dataset.
type DemoRepo struct {
	Name        string
	Description string
	Public      bool // anonymous read
}

// SeedFunc inserts synthetic rows for the scale test ("telesphorepo-server seed"). It adds n rows to
// repo, a repo of the module's format, numbered from start so that their paths are unique and a
// repeated call is harmless. The caller opens the transaction and calls it in batches.
type SeedFunc func(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error

// Extender is the manifest of one module while Register runs.
type Extender struct{ m *Manifest }

// Schema declares the module's tables and queries. The component is named after the module.
// At most one call.
func (x *Extender) Schema(c db.Component) {
	if x.m.Schema != nil {
		panic(fmt.Sprintf("module %s: Schema twice", x.m.Module.ID))
	}
	c.Name = x.m.Module.ID
	x.m.Schema = &c
}

// Settings adds a group of settings that the module owns (see settings.Group). One call per group.
func (x *Extender) Settings(g *settings.Group) {
	g.Module = x.m.Module.ID
	x.m.Settings = append(x.m.Settings, g)
}

// Permissions adds a permission subsystem of the module and its permissions (see perm.Def). One
// call per subsystem.
func (x *Extender) Permissions(s perm.Subsystem, defs ...perm.Def) {
	x.m.Permissions = append(x.m.Permissions, Permissions{s, defs})
}

// AuditActions adds the audit actions that the module records (see audit.ActionDef).
func (x *Extender) AuditActions(defs ...audit.ActionDef) {
	x.m.AuditActions = append(x.m.AuditActions, defs...)
}

// OpType adds a type of repo operation that the module begins (see ops.Type).
func (x *Extender) OpType(t ops.Type) { x.m.OpTypes = append(x.m.OpTypes, t) }

// Init runs fn at startup, in module order, after the kernel services are ready. An error stops
// startup. The closures that Routes, Job, Format and the other calls register run only after
// every Init of the module ran.
func (x *Extender) Init(fn func(ctx context.Context, h *Host) error) {
	x.m.Inits = append(x.m.Inits, fn)
}

// Routes mounts the module's routes. A module owns /api/, the URL segment of each format it
// registers, and its RootPrefix claims.
func (x *Extender) Routes(fn func(r *route.Router)) { x.m.Routes = append(x.m.Routes, fn) }

// RootPrefix claims a path outside /api/ and the format segments, e.g. "/v2/" or "/setup.sh".
func (x *Extender) RootPrefix(prefix string) { x.m.RootPrefixes = append(x.m.RootPrefixes, prefix) }

// Job adds a background job (see jobs.Job). Its name starts with "<module ID>.".
func (x *Extender) Job(j jobs.Job) {
	if len(j.Name) <= len(x.m.Module.ID)+1 || j.Name[:len(x.m.Module.ID)+1] != x.m.Module.ID+"." {
		panic(fmt.Sprintf("module %s: job name %q must start with %q", x.m.Module.ID, j.Name, x.m.Module.ID+"."))
	}
	x.m.Jobs = append(x.m.Jobs, j)
}

// Format adds a repository format. The module owns the path "/<name>/".
func (x *Extender) Format(s format.Spec) { x.m.Formats = append(x.m.Formats, s) }

// RepoField adds a repo value that the module owns, e.g. "signing_key" (see repos.Field). The
// repo service uses it after the module's Init ran.
func (x *Extender) RepoField(f repos.Field) { x.m.RepoFields = append(x.m.RepoFields, f) }

// CIProvider adds a CI system whose tokens make build records and whose trust policies the
// access report describes (see ci.Provider).
func (x *Extender) CIProvider(p ci.Provider) { x.m.CIProviders = append(x.m.CIProviders, p) }

// PublisherClient adds a publisher client that enrolls devices (see clients.Client).
func (x *Extender) PublisherClient(c clients.Client) { x.m.Clients = append(x.m.Clients, c) }

// RepoGuard adds an extra rule to repo access (access.Guard), e.g. the IP rules of the network
// module. The access engine calls g only after the module's Init ran.
func (x *Extender) RepoGuard(g access.Guard) { x.m.Guards = append(x.m.Guards, g) }

// ResolveClient resolves the client of every request once, before any handler runs: its address
// and its scheme. Exactly one module may set it; without one the client is the TCP peer and the
// scheme is that of the connection.
func (x *Extender) ResolveClient(fn func(r *http.Request) httpx.Client) {
	if x.m.ResolveClient != nil {
		panic(fmt.Sprintf("module %s: ResolveClient twice", x.m.Module.ID))
	}
	x.m.ResolveClient = fn
}

// TokenKind registers the verifier of the tokens with prefix p (see auth.Verifier).
func (x *Extender) TokenKind(p token.Prefix, v auth.Verifier) {
	x.m.TokenKinds = append(x.m.TokenKinds, TokenKind{p, v})
}

// Demo adds the module's part of the dev demo dataset. The module registers exactly one format.
func (x *Extender) Demo(d DemoData) { x.m.Demo = &d }

// Seed adds the module's rows of the scale test. The module registers exactly one format.
func (x *Extender) Seed(fn SeedFunc) { x.m.Seed = fn }

// Build checks mods and runs every Register, in order. It panics on a manifest that breaks the
// contract: demo data or a seed without exactly one format, or two modules that resolve
// the client.
func Build(mods []Module) []*Manifest {
	Check(mods)
	out := make([]*Manifest, 0, len(mods))
	address := ""
	for _, m := range mods {
		mf := &Manifest{Module: m}
		m.Register(&Extender{m: mf})
		if (mf.Demo != nil || mf.Seed != nil) && len(mf.Formats) != 1 {
			panic(fmt.Sprintf("module %s: demo data and seeds need exactly one format", m.ID))
		}
		for _, f := range mf.Formats {
			if f.SigningKey && !slices.Contains(m.Requires, "signing") {
				panic(fmt.Sprintf("module %s: format %s binds signing keys, so the module requires signing", m.ID, f.Name))
			}
		}
		if mf.ResolveClient != nil {
			if address != "" {
				panic(fmt.Sprintf("module %s: module %s resolves the client already", m.ID, address))
			}
			address = m.ID
		}
		out = append(out, mf)
	}
	return out
}

// FormatName returns the name of the module's only format ("" when it has none or several).
func (m *Manifest) FormatName() string {
	if len(m.Formats) != 1 {
		return ""
	}
	return m.Formats[0].Name
}

// Prefixes returns every path prefix the module owns outside /api/: its format segments and its
// root prefixes.
func (m *Manifest) Prefixes() []string {
	out := slices.Clone(m.RootPrefixes)
	for _, f := range m.Formats {
		out = append(out, "/"+f.Name+"/")
	}
	return out
}
