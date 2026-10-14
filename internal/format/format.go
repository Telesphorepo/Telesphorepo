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
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
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

// DeleteObserver runs inside the transaction that deletes asset rows of repo (spec §13), before
// the rows are deleted, so it can still read them. It removes the format's own rows that point
// at the files, e.g. Debian removes the packages and marks their indexes for a rebuild. While a
// whole repo is deleted, repo.LockedBy is RepoDeleting.
type DeleteObserver interface {
	AssetsDeleted(ctx context.Context, tx *sql.Tx, repo *repos.Repo, paths []string) error
}

// RepoDeleting is the LockedBy text of a repo while it is deleted.
const RepoDeleting = "the repository is being deleted"

// ManualUploader takes files uploaded through the web UI (POST /api/repos/{name}/upload, spec
// §13). fields are the form's text fields; userID is the uploader (0 for a CI session). A
// client mistake is an *UploadError.
//
// Scan (POST /api/repos/{name}/upload/scan, spec §13.1) looks at the files of a dropped folder
// and returns what an Upload of them would store, one Found per artifact or package, with the
// problems that would refuse it. It stores nothing. Files that no Found lists are ignored.
type ManualUploader interface {
	Upload(ctx context.Context, repo *repos.Repo, files []UploadFile, fields map[string]string, userID int64) ([]Uploaded, error)
	Scan(ctx context.Context, repo *repos.Repo, files []UploadFile) ([]Found, error)
}

// UploadFile is one uploaded file: its base name, its path relative to the dropped folder
// (forward slashes; the base name for a single file) and its spooled body.
type UploadFile struct {
	Name  string
	Path  string
	Spool *assets.Spooled
}

// InFolder reports whether any file came with a folder path, i.e. the client dropped a folder.
func InFolder(files []UploadFile) bool {
	for _, f := range files {
		if strings.Contains(f.Path, "/") {
			return true
		}
	}
	return false
}

// Found is one artifact or package that a scan found: Kind is the format's word for it (e.g.
// "maven", "debian", "file"), Title names it (e.g. "com.prism:api:1.2"), Files are the
// relative paths it takes, and Problems say why an upload would refuse it (empty: it is fine).
type Found struct {
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Files    []string `json:"files"`
	Problems []string `json:"problems"`
}

// Uploaded is one file a manual upload stored or found unchanged. Generated files (checksums,
// metadata) are listed too.
type Uploaded struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "created", "replaced" or "unchanged"
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Upload statuses.
const (
	UploadCreated   = "created"
	UploadReplaced  = "replaced"
	UploadUnchanged = "unchanged"
)

// UploadError is a client mistake in a manual upload: Status is 400 or 409.
type UploadError struct {
	Status  int
	Message string
}

func (e *UploadError) Error() string { return e.Message }

// BadUpload returns a 400 UploadError.
func BadUpload(format string, args ...any) error {
	return &UploadError{http.StatusBadRequest, fmt.Sprintf(format, args...)}
}

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
	Log      *slog.Logger   // tagged with component=<format name>
	Audit    *audit.Service // records uploads on the format routes
}

// RecordUpload writes an upload through a format route to the audit log.
func (h Host) RecordUpload(r *http.Request, p *auth.Principal, repo *repos.Repo, path string, size int64, sha256 string) {
	h.Audit.Record(r.Context(), r, audit.Event{
		Actor: auth.ActorOf(p), Action: audit.Upload, RepoID: repo.ID, Repo: repo.Name, Target: path,
		Detail: map[string]any{"size": size, "sha256": sha256},
	})
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
