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
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/provenance"
	"github.com/Telesphorepo/Telesphorepo/internal/proxy"
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

// Grouper marks a format that serves group repos: its read route answers a read of a group from
// the group's members (Host.Repos.MemberRepos), in order, and skips a member that the reader
// cannot read (Host.Auth.ReadsThrough). The core refuses a group of a format without it.
type Grouper interface {
	ServesGroups()
}

// Proxied marks a format that serves proxy repos. Classify tells the core how to cache a path;
// ok=false refuses the path (404). The read route of the format passes a proxy repo's reads to
// Host.Proxy. ProxyProbe is the path that a check of the remote requests: "" for the remote's
// root, or a file that the remote of a repo with config must have. The core refuses a proxy of
// a format without it.
type Proxied interface {
	Classify(path string) (c ProxyClass, ok bool)
	ProxyProbe(config json.RawMessage) string
}

// ProxyClass is how a proxy caches a path.
type ProxyClass int

const (
	ProxyImmutable ProxyClass = iota // fetched once, never checked again (a release jar, a .deb)
	ProxyChanging                    // checked after metadata_ttl (maven-metadata.xml, InRelease)
)

// SupportsKind returns an error when format name takes no repos of kind (repos.KindCheck).
func SupportsKind(name, kind string) error {
	f := Get(name)
	if f == nil {
		return fmt.Errorf("unknown format %q; available formats: %s", name, strings.Join(Names(), ", "))
	}
	ok := false
	switch kind {
	case repos.KindHosted:
		ok = true
	case repos.KindGroup:
		_, ok = f.(Grouper)
	case repos.KindProxy:
		_, ok = f.(Proxied)
	}
	if !ok {
		return fmt.Errorf("%s repositories cannot be a %s", f.Title(), kind)
	}
	return nil
}

// Kinds returns the repo kinds that format f takes.
func Kinds(f Format) []string {
	out := []string{repos.KindHosted}
	if _, ok := f.(Proxied); ok {
		out = append(out, repos.KindProxy)
	}
	if _, ok := f.(Grouper); ok {
		out = append(out, repos.KindGroup)
	}
	return out
}

// Iconic supplies the format's icons for the UI: SVGs of one color (currentColor) on a 24x24
// view box that read at 16 px. GET /api/formats returns them inline. Icon is the format's mark.
// KindIcons maps an Artifact.Kind to the mark of artifacts that differ from the format, e.g. a
// Gradle plugin in a Maven repo; nil when there are none.
type Iconic interface {
	Icon() []byte
	KindIcons() map[string][]byte
}

// Describer returns the repo-level snippets of a repo (UI and "telesphorepo dev"): how to use the
// repo in a build (Purpose "use") and how to publish to it (Purpose "publish"), each with Context
// "repo". baseURL is the server's public URL without a trailing slash.
type Describer interface {
	Usage(repo *repos.Repo, baseURL string) []Snippet
}

// ArtifactDescriber returns the snippets that use one artifact of a repo (Context "artifact"):
// the dependency line with the repository block it needs. path is an artifact folder or a
// version folder (a file path selects its folder). mode is VersionPinned or VersionLatest. A
// path that is not an artifact gives a nil *ArtifactUsage.
type ArtifactDescriber interface {
	ArtifactUsage(ctx context.Context, repo *repos.Repo, baseURL, path, mode string) (*ArtifactUsage, error)
}

// Version modes of ArtifactDescriber.
const (
	VersionPinned = "pinned" // this version, e.g. 1.4.2
	VersionLatest = "latest" // always the newest release
)

// ArtifactUsage is what ArtifactDescriber returns.
type ArtifactUsage struct {
	Artifact Artifact  `json:"artifact"`
	Version  string    `json:"version"`      // the version the pinned snippets name; "" when none
	Mode     string    `json:"version_mode"` // VersionPinned or VersionLatest
	Snippets []Snippet `json:"snippets"`
}

// Artifact names an artifact of a repo: Path is its folder (with a trailing slash), Title its
// coordinates (e.g. "com.acme:lib"), Kind the format's word for it (e.g. "library", "bom",
// "gradle-plugin").
type Artifact struct {
	Path  string `json:"path"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

// Versioned groups the files of an artifact folder into versions, and the files of a version
// folder into builds (e.g. the timestamped builds of a Maven -SNAPSHOT version). Both return a
// nil *Artifact when path is not such a folder. Versions are newest first; after is the last
// version of the previous page ("" for the first). Builds are newest first; after is the last
// build of the previous page. next is "" at the end.
type Versioned interface {
	Versions(ctx context.Context, repo *repos.Repo, path, after string, limit int) (*Artifact, []Version, string, error)
	Builds(ctx context.Context, repo *repos.Repo, path, after string, limit int) (*Artifact, *Version, []Build, string, error)
}

// Version kinds.
const (
	VersionRelease  = "release"
	VersionSnapshot = "snapshot"
)

// Version is one version of an artifact. A release has one build; a snapshot has one or more,
// and LatestBuild names the newest. PublishedAt is when its newest build was published, and
// Provenance is that build's (nil: none).
type Version struct {
	Version     string                 `json:"version"`
	Path        string                 `json:"path"` // the version folder, with a trailing slash
	Kind        string                 `json:"kind"` // VersionRelease or VersionSnapshot
	LatestBuild string                 `json:"latest_build,omitempty"`
	BuildsCount int                    `json:"builds_count"`
	PublishedAt time.Time              `json:"published_at"`
	Provenance  *provenance.Provenance `json:"provenance"`
}

// Build is one build of a version: its id (a snapshot's "20261014.103000-3", a release's
// version), its files without checksum and signature files, when it was published and its
// provenance (nil: none).
type Build struct {
	Build       string                 `json:"build"`
	Files       []BuildFile            `json:"files"`
	PublishedAt time.Time              `json:"published_at"`
	Provenance  *provenance.Provenance `json:"provenance"`
}

// BuildFile is one file of a build. Classifier and Extension are the format's split of its name
// (e.g. "sources" and "jar"); Classifier is "" for the main file.
type BuildFile struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Classifier string `json:"classifier"`
	Extension  string `json:"extension"`
}

// DemoData fills the plugin's part of the demo dataset of "telesphorepo dev". DemoRepos lists
// the repos it fills; the command creates them, gives users access, and calls Demo once for each
// repo in that order, with a generator seeded by the repo's place in the dataset. Demo stores the
// files through run (run.Add, or the plugin's own upload pipeline and run.Stored) and announces
// how many it will store with run.Expect.
type DemoData interface {
	DemoRepos() []DemoRepo
	Demo(ctx context.Context, run *demo.Run, repo *repos.Repo, rng *rand.Rand) error
}

// DemoRepo is a repo of the demo dataset.
type DemoRepo struct {
	Name        string
	Description string
	Public      bool // anonymous read
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

// ImportItem is one source file. CreatedAt and UpdatedAt are the source's dates of the file;
// zero means now. The plugin stores the file with them (assets.By), and gives a file that it
// skips the same dates (assets.Service.SetTimes). A file the plugin generates keeps now.
type ImportItem struct {
	Path      string // source path, forward slashes
	Size      int64
	SHA256    string // lowercase hex; "" when the source does not give one
	CreatedAt time.Time
	UpdatedAt time.Time
	Open      func(ctx context.Context) (io.ReadCloser, error)
}

// By returns who writes the item (nobody: an import) with the item's dates.
func (it ImportItem) By() assets.By {
	return assets.By{Created: it.CreatedAt, Updated: it.UpdatedAt}
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
// §13). fields are the form's text fields; by is who uploads (see ByOf). A
// client mistake is an *UploadError.
//
// Scan (POST /api/repos/{name}/upload/scan, spec §13.1) looks at the files of a dropped folder
// and returns what an Upload of them would store, one Found per artifact or package, with the
// problems that would refuse it. It stores nothing. Files that no Found lists are ignored.
type ManualUploader interface {
	Upload(ctx context.Context, repo *repos.Repo, files []UploadFile, fields map[string]string, by assets.By) ([]Uploaded, error)
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

// Snippet is one copy-paste step. The UI shows one tab per Client and the snippets of a client
// in order.
type Snippet struct {
	// Client is the tool the snippet is for: "gradle-kotlin", "gradle-groovy", "maven", "apt",
	// "curl", "github-actions" or "jenkins".
	Client string `json:"client"`
	// Purpose is SnippetUse (add the repo or artifact to a build) or SnippetPublish.
	Purpose string `json:"purpose"`
	// Context is SnippetRepo (repo setup) or SnippetArtifact (one artifact).
	Context string `json:"context"`
	// Via says where a publish snippet publishes from: ViaComputer (a client plugin with a
	// device), ViaCI or ViaHand (the browser, or a publish token with curl). "" for a use snippet.
	Via   string `json:"via,omitempty"`
	Title string `json:"title"` // the file or step, e.g. "~/.m2/settings.xml"
	Lang  string `json:"lang"`  // highlighting hint: "xml", "kotlin", "groovy", "sh", "yaml", ...
	Code  string `json:"code"`
	Note  string `json:"note,omitempty"` // one line under the code, e.g. a trade-off
}

// Snippet purposes, contexts and publish sources.
const (
	SnippetUse      = "use"
	SnippetPublish  = "publish"
	SnippetRepo     = "repo"
	SnippetArtifact = "artifact"

	ViaComputer = "computer"
	ViaCI       = "ci"
	ViaHand     = "hand"
)

// Host is the set of core services a format may use. Plugins never import each other.
type Host struct {
	DB       *db.DB
	Auth     *auth.Service     // Identify, RepoGate (pass the format's Name), Access
	Assets   *assets.Service   // Spool, Lock, Put, Serve, upserts in a transaction
	Repos    *repos.Service    // repos, group members and proxy remotes (in memory)
	Proxy    *proxy.Service    // fetches and serves the files of proxy repos
	Config   config.Provider   // live core configuration
	Settings *settings.Service // live snapshot (Current) with the format's own settings; Subscribe
	Keys     *signing.Keys     // the repository signing keys (live snapshot)
	Log      *slog.Logger      // tagged with component=<format name>
	Audit    *audit.Service    // records uploads on the format routes
}

// ByOf returns who writes the files of an upload by p: the user, and the publish session when it
// recorded build provenance.
func ByOf(p *auth.Principal) assets.By {
	if p == nil {
		return assets.By{}
	}
	by := assets.By{User: p.UserID}
	if p.Publish != nil && p.Publish.Provenance {
		by.Session = p.Publish.ID
	}
	return by
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
