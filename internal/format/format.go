// Package format is the plugin contract for repository formats (spec §5.4). A format is one
// package that implements Format and is listed once in internal/formats/all.go; the core finds
// its optional capabilities by type assertion and never names a specific format.
package format

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// Format is a repository format plugin.
type Format interface {
	// Name is the URL segment, the repos.format value, the settings group and the migration
	// component, e.g. "maven".
	Name() string
	// Title is the display name, e.g. "Maven / Gradle".
	Title() string
	// DefaultConfig is the repo config (a JSON object) of a new repo created without one.
	DefaultConfig() json.RawMessage
	// ValidateConfig validates a repo config (a JSON object) and returns its normalized form.
	ValidateConfig(raw json.RawMessage) (json.RawMessage, error)
	// Register mounts the format's routes: /{name}/{repo}/... and any extra paths it owns.
	Register(mux *http.ServeMux, h Host)
}

// Starter runs background workers. Start returns immediately; workers stop when ctx ends.
type Starter interface {
	Start(ctx context.Context)
}

// Migrator owns tables: Migrations holds NNNN_*.sql files at its root, applied after core as
// the migration track named after the format.
type Migrator interface {
	Migrations() fs.FS
}

// Configurable contributes server settings. They form one settings group named after the
// format and labelled with its title; each Def's Group is set by the core.
type Configurable interface {
	Settings() []settings.Def
}

// StatusReporter reports a repo's processing state for GET /api/repos/{name}/status, e.g.
// {"pending": 3}.
type StatusReporter interface {
	Status(ctx context.Context, repo *repos.Repo) (any, error)
}

// ConfigObserver runs after a repo's config was changed through the API.
type ConfigObserver interface {
	RepoConfigChanged(ctx context.Context, old, new *repos.Repo) error
}

// Describer returns the client setup snippets for a repo (UI and "telesphorepo dev").
// baseURL is the server's public URL without a trailing slash.
type Describer interface {
	Usage(repo *repos.Repo, baseURL string) []Snippet
}

// Queryable lists the format's queries for the plan guard (telesphorepo dbcheck).
type Queryable interface {
	QuerySet() db.QuerySet
}

// Seeder inserts synthetic rows for the scale test (telesphorepo seed). Seed adds n rows to repo,
// numbered from start so that their paths are unique and a repeated call is harmless. It may
// use the format's own queries and the core asset queries. The caller opens the transaction
// and runs Seed in batches.
type Seeder interface {
	Seed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error
}

// Importer takes files from another repository manager into a repo of this format (spec §12).
// The import runner names no format: it offers each source asset to the target repo's plugin.
type Importer interface {
	// ImportFormats lists the source format ids the plugin accepts, e.g. "maven2", "apt", "raw".
	ImportFormats() []string
	// Import stores one source file in repo. An error fails this item only; the runner records
	// it and goes on.
	Import(ctx context.Context, repo *repos.Repo, item ImportItem) (ImportOutcome, error)
}

// ImportItem is one source file.
type ImportItem struct {
	Path   string // source path, forward slashes
	Size   int64
	SHA256 string // lowercase hex; "" when the source does not give one
	Open   func(ctx context.Context) (io.ReadCloser, error)
}

// ImportOutcome is what Import did with an item.
type ImportOutcome string

const (
	ImportImported ImportOutcome = "imported" // stored
	ImportSkipped  ImportOutcome = "skipped"  // already present with the same bytes
	ImportIgnored  ImportOutcome = "ignored"  // not a file of this format, e.g. a source index
)

// Snippet is one client setup step.
type Snippet struct {
	Title string `json:"title"` // e.g. "~/.m2/settings.xml"
	Lang  string `json:"lang"`  // highlighting hint: "xml", "kotlin", "sh", ...
	Code  string `json:"code"`
}

// Host is the set of core services a format may use. Plugins never import each other.
type Host struct {
	DB       *db.DB
	Auth     *auth.Service   // Identify, RepoGate (pass the format's Name), Access
	Assets   *assets.Service // Spool, Lock, Put, Serve, upserts in a transaction
	Repos    *repos.Service
	Config   config.Provider   // live core configuration
	Settings *settings.Service // live snapshot (Current) with the format's own settings; Subscribe
	Signer   func() *signing.Signer
	Log      *slog.Logger // tagged with component=<format name>
}

var (
	mu     sync.Mutex
	ctors  []func() Format
	built  []Format
	byName map[string]Format
)

// Register adds format constructors. internal/formats calls it from init.
func Register(c ...func() Format) {
	mu.Lock()
	defer mu.Unlock()
	if built != nil {
		panic("format: Register after All")
	}
	ctors = append(ctors, c...)
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// reserved names collide with core URL segments or the core migration track.
var reserved = map[string]bool{"api": true, "core": true, "healthz": true}

func build() {
	byName = map[string]Format{}
	built = make([]Format, 0, len(ctors))
	for _, c := range ctors {
		f := c()
		n := f.Name()
		if !nameRE.MatchString(n) || reserved[n] {
			panic(fmt.Sprintf("format: invalid name %q", n))
		}
		if byName[n] != nil {
			panic(fmt.Sprintf("format: %q registered twice", n))
		}
		byName[n] = f
		built = append(built, f)
	}
}

// All returns every registered format in registration order. Each constructor runs once per
// process; later calls return the same instances.
func All() []Format {
	mu.Lock()
	defer mu.Unlock()
	if built == nil {
		build()
	}
	return built
}

// Get returns the format called name, or nil.
func Get(name string) Format {
	All()
	return byName[name]
}

// Names returns the registered format names in registration order.
func Names() []string {
	all := All()
	out := make([]string, len(all))
	for i, f := range all {
		out[i] = f.Name()
	}
	return out
}

// ValidateRepoConfig validates the repo config raw for format name (repos.Validator). An empty
// raw or null selects the format's DefaultConfig.
func ValidateRepoConfig(name string, raw json.RawMessage) (json.RawMessage, error) {
	f := Get(name)
	if f == nil {
		return nil, fmt.Errorf("unknown format %q; available formats: %s", name, strings.Join(Names(), ", "))
	}
	if t := bytes.TrimSpace(raw); len(t) == 0 || string(t) == "null" {
		raw = f.DefaultConfig()
	}
	return f.ValidateConfig(raw)
}
