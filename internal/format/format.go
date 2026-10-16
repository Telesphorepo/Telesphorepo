// Package format is the contract of a repository format. A format is a Spec that a module passes
// to module.Extender.Format. Every optional capability is a field that is nil when the format
// does not have it, so the core reads which capabilities a format has and never names a format.
package format

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/rebuild"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// Spec is one repository format.
type Spec struct {
	// Name is the URL segment ("/maven/"), the repos.format value and the name in the UI, e.g.
	// "maven". It has the form of a module ID.
	Name string
	// Title is the display name, e.g. "Maven / Gradle".
	Title string
	// Description says in one sentence what the repos of the format hold.
	Description string
	// Icon is the format's mark: an SVG of one color (currentColor) on a 24x24 view box that reads
	// at 16 px. GET /api/formats returns it inline.
	Icon []byte
	// KindIcons maps an Artifact.Kind to the mark of artifacts that differ from the format, e.g. a
	// Gradle plugin in a Maven repo.
	KindIcons map[string][]byte
	// Kinds are the repo kinds the format takes: "hosted" always, "proxy" (needs Classify and
	// ProxyProbe) and "group" (any format: the core read path serves it).
	Kinds []string

	// ValidName checks the name of a new repo of the format beyond the rules of every repo name,
	// e.g. one segment of an OCI repository name. nil: no extra rule.
	ValidName func(name string) error
	// Config is the repo config of the format.
	Config ConfigSpec
	// SigningKey makes each hosted repo of the format bind a signing key (the repo field signing_key of
	// the signing module, which the format's module requires).
	SigningKey bool
	// RepoTabs are the tabs of other modules that the repo settings of the format show, e.g.
	// "signing".
	RepoTabs []string

	// Routes mounts the routes of the format under "/<Name>/" and any root prefix its module
	// claims. Each route has its policy, usually route.FormatRead or route.FormatWrite with the
	// format's Name, and stores and serves through the asset pipeline and the read path (Kit).
	// nil mounts Standard: GET and HEAD /<Name>/{repo}/{path...} through the read path and PUT
	// through Commit with source "put".
	Routes func(r *route.Router, k Kit)

	// Asset hooks (assets.Hooks). Describe gives the content type and the kind of a path (nil:
	// application/octet-stream, artifact); Admit decides a write (nil: the same bytes are kept,
	// other bytes replace the file); OnCommitted and OnRemoved write the format's own rows in the
	// transaction of a write or a remove; AfterCommit and AfterRemove run after it.
	Describe    func(path repopath.Path) (assets.Describe, bool)
	Admit       func(ctx context.Context, w *assets.Write, existing *assets.Asset) (assets.Decision, error)
	OnCommitted func(ctx context.Context, tx *sql.Tx, w *assets.Write, a *assets.Asset, prev *assets.Asset) error
	AfterCommit func(ctx context.Context, repo *repos.Repo, rs []*assets.Result)
	OnRemoved   func(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*assets.Asset, prefixes []repopath.Path) ([]repopath.Path, error)
	AfterRemove func(ctx context.Context, repo *repos.Repo, removed []*assets.Asset)
	// OnRepoChange runs in the transaction of every repo update (API and Nexus mapping), after the
	// rows changed, with the repo before and after.
	OnRepoChange func(ctx context.Context, tx *sql.Tx, old, new *repos.Repo) error

	// Classify tells the proxy how to cache a path of a proxy with the remote remote; ok=false
	// refuses the path (404).
	Classify func(remote *repos.Proxy, path repopath.Path) (assets.Class, bool)
	// ProxyProbe is the path that a check of a proxy's remote requests: "" for the remote's root,
	// or a file that the remote of a repo with config must have.
	ProxyProbe func(config json.RawMessage) string
	// Merge merges a file that several members of a group have (e.g. maven-metadata.xml).
	Merge *assets.Merger
	// RemoteAuth answers a remote's 401 challenge with the headers of a retry (e.g. the Docker
	// Hub token exchange). The proxy calls it once per fetch. nil: the proxy sends the remote's
	// credentials as HTTP Basic on every request.
	RemoteAuth func(ctx context.Context, remote *repos.Proxy, challenge http.Header) (http.Header, error)

	// Rebuild builds the units of a group that the format marked with rebuild.Queue.MarkTx (e.g.
	// the Debian indexes of a distribution).
	Rebuild *Rebuild

	// Versions and Builds group the files of an artifact into versions, and the files of a
	// version into builds (e.g. the timestamped builds of a Maven -SNAPSHOT version). A version
	// is named by its artifact folder and its version string, so a format that keeps every
	// version in one folder (the Debian pool) fits as well as one with a folder per version
	// (Maven). Versions returns a nil *Artifact when artifactPath is not an artifact folder;
	// Builds returns a nil *Artifact or *Version when the artifact has no such version. Versions
	// are newest first; after is the last version of the previous page ("" for the first). Builds
	// are newest first; after is the last build of the previous page. next is "" at the end. A
	// format sets both or neither.
	Versions func(ctx context.Context, repo *repos.Repo, artifactPath, after string, limit int) (*Artifact, []Version, string, error)
	Builds   func(ctx context.Context, repo *repos.Repo, artifactPath, version, after string, limit int) (*Artifact, *Version, []Build, string, error)

	// Usage returns the repo-level snippets of a repo (UI and "telesphorepo dev"): how to use the
	// repo in a build (Purpose "use") and how to publish to it from a computer and by hand
	// (Purpose "publish"), each with Context "repo". The core adds the CI files of Tools.
	// baseURL is the server's public URL without a trailing slash.
	Usage func(repo *repos.Repo, baseURL string) []Snippet
	// Tools are the build tools that publish to the format's repos: the core builds their CI
	// files (GitHub Actions and Jenkins) and the project setup scripts from them.
	Tools []Tool
	// ArtifactUsage returns the snippets that use one artifact of a repo (Context "artifact"):
	// the dependency or install line with the repository setup it needs. path is an artifact
	// folder, a version folder or a file of a version. version names a version of the artifact
	// folder path ("" for none), as Versions names it. mode is VersionPinned or VersionLatest. A
	// path that is not an artifact gives a nil *ArtifactUsage.
	ArtifactUsage func(ctx context.Context, repo *repos.Repo, baseURL, path, version, mode string) (*ArtifactUsage, error)

	// Upload takes files uploaded through the web UI.
	Upload *UploadSpec
	// Importer takes files from another repository manager.
	Importer *Importer

	// UI is what the web UI shows of the format (GET /api/formats).
	UI UIDescriptor
}

// Rebuild is the rebuild of a format (see rebuild.Run).
type Rebuild struct {
	Run rebuild.Run
}

// ConfigSpec is the repo config of a format: a JSON object.
type ConfigSpec struct {
	// Default is the config of a new repo created without one.
	Default json.RawMessage
	// Fields are the options of the config that the web UI edits, one form field per key.
	Fields []Field
	// Validate validates a config and returns its normalized form.
	Validate func(raw json.RawMessage) (json.RawMessage, error)
}

// Takes reports whether the format takes repos of kind.
func (s *Spec) Takes(kind string) bool { return slices.Contains(s.Kinds, kind) }

// Hooks returns the asset hooks of the format.
func (s *Spec) hooks() *assets.Hooks {
	return &assets.Hooks{Describe: s.Describe, Admit: s.Admit, OnCommitted: s.OnCommitted, AfterCommit: s.AfterCommit,
		OnRemoved: s.OnRemoved, AfterRemove: s.AfterRemove, Classify: s.Classify, Merge: s.Merge, RemoteAuth: s.RemoteAuth}
}

// Version modes of ArtifactUsage.
const (
	VersionPinned = "pinned" // this version, e.g. 1.4.2
	VersionLatest = "latest" // always the newest release
)

// ArtifactUsage is what Spec.ArtifactUsage returns.
type ArtifactUsage struct {
	Artifact Artifact  `json:"artifact"`
	Version  string    `json:"version"`      // the version the pinned snippets name; "" when none
	Mode     string    `json:"version_mode"` // VersionPinned or VersionLatest
	Snippets []Snippet `json:"snippets"`
}

// Artifact names an artifact of a repo: Path is its folder (with a trailing slash), Title its
// coordinates (e.g. "com.acme:lib"), Name its short name for a heading (e.g. "lib", a plugin ID),
// Kind the format's word for it (e.g. "library", "bom", "gradle-plugin").
type Artifact struct {
	Path  string `json:"path"`
	Title string `json:"title"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
}

// Version kinds.
const (
	VersionRelease  = "release"
	VersionSnapshot = "snapshot"
)

// Version is one version of an artifact. A release has one build; a snapshot has one or more,
// and LatestBuild names the newest. LatestLabel is the format's name for that build for people
// ("Build 3"); the UI shows LatestBuild when it is empty. PublishedAt is when its newest build
// was published, and CI is the CI build record of that build (nil: none).
type Version struct {
	Version     string    `json:"version"`
	Kind        string    `json:"kind"` // VersionRelease or VersionSnapshot
	LatestBuild string    `json:"latest_build,omitempty"`
	LatestLabel string    `json:"latest_label,omitempty"`
	BuildsCount int       `json:"builds_count"`
	PublishedAt time.Time `json:"published_at"`
	CI          *ci.Build `json:"build"`
}

// Build is one build of a version: its id (a snapshot's "20261014.103000-3", a release's
// version), the format's name for it for people ("Build 3"; empty: the UI shows the id), its
// files without checksum and signature files, when it was published and the CI build record that
// wrote it (nil: none).
type Build struct {
	Build       string      `json:"id"`
	Label       string      `json:"label,omitempty"`
	Files       []BuildFile `json:"files"`
	PublishedAt time.Time   `json:"published_at"`
	CI          *ci.Build   `json:"build"`
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

// OriginOf returns who writes the files of a request by p with source s: the user, and the CI
// build record of p's publish session (none for a session without one).
func OriginOf(p *auth.Principal, s assets.Source) assets.Origin {
	o := assets.Origin{Source: s}
	if p == nil {
		return o
	}
	o.UserID = p.UserID
	if p.Is(auth.CredPublish) {
		o.BuildID = p.Publish.BuildID
	}
	return o
}

// Registry holds the formats of every module, in module order.
type Registry struct {
	all    []*Spec
	byName map[string]*Spec
	hooks  map[string]*assets.Hooks
}

var (
	nameRE    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	setupIDRE = regexp.MustCompile(`^[a-z][a-z0-9]{0,31}$`)
)

// reserved names collide with paths of the server or with the kernel's schema component.
var reserved = map[string]bool{"api": true, "core": true, "healthz": true, "assets": true, "setup": true}

// NewRegistry checks the specs and returns their registry. A spec breaks the contract when its
// name is not valid or is taken, when it has no Config.Validate, when its Kinds lack "hosted"
// or name another kind, when it takes proxies without Classify and ProxyProbe, when its Merge
// lacks Match or Merge, when it sets one of Versions and Builds without the other, or when its
// Upload, Importer or Rebuild is incomplete.
func NewRegistry(specs []Spec) (*Registry, error) {
	r := &Registry{byName: map[string]*Spec{}, hooks: map[string]*assets.Hooks{}}
	for i := range specs {
		s := &specs[i]
		if err := check(s); err != nil {
			return nil, fmt.Errorf("format %q: %w", s.Name, err)
		}
		if r.byName[s.Name] != nil {
			return nil, fmt.Errorf("format %q: registered twice", s.Name)
		}
		r.byName[s.Name] = s
		r.hooks[s.Name] = s.hooks()
		r.all = append(r.all, s)
	}
	return r, nil
}

func check(s *Spec) error {
	switch {
	case !nameRE.MatchString(s.Name) || reserved[s.Name]:
		return fmt.Errorf("invalid name")
	case s.Config.Validate == nil:
		return fmt.Errorf("no Config.Validate")
	case !s.Takes(repos.KindHosted):
		return fmt.Errorf("Kinds must hold %q", repos.KindHosted)
	case s.Takes(repos.KindProxy) && (s.Classify == nil || s.ProxyProbe == nil):
		return fmt.Errorf("a proxy format needs Classify and ProxyProbe")
	case s.Merge != nil && (s.Merge.Match == nil || s.Merge.Merge == nil):
		return fmt.Errorf("Merge needs Match and Merge")
	case (s.Versions == nil) != (s.Builds == nil):
		return fmt.Errorf("set both Versions and Builds, or neither")
	case s.Upload != nil && (s.Upload.Store == nil || s.Upload.Plan == nil):
		return fmt.Errorf("Upload needs Plan and Store")
	case s.Importer != nil && (len(s.Importer.Sources) == 0 || s.Importer.Target == nil):
		return fmt.Errorf("Importer needs Sources and Target")
	case s.Rebuild != nil && s.Rebuild.Run == nil:
		return fmt.Errorf("Rebuild needs Run")
	}
	for _, k := range s.Kinds {
		if k != repos.KindHosted && k != repos.KindProxy && k != repos.KindGroup {
			return fmt.Errorf("unknown kind %q", k)
		}
	}
	for i, t := range s.Tools {
		switch {
		case !nameRE.MatchString(t.ID) || t.Label == "" || t.Publish == nil:
			return fmt.Errorf("tool %q needs an ID, a Label and Publish", t.ID)
		case t.Setup != nil && !setupIDRE.MatchString(t.ID):
			return fmt.Errorf("tool %q: a tool with Setup has an ID of lower-case letters and digits", t.ID)
		case slices.ContainsFunc(s.Tools[:i], func(o Tool) bool { return o.ID == t.ID }):
			return fmt.Errorf("tool %q registered twice", t.ID)
		}
	}
	return nil
}

// All returns every format in module order.
func (r *Registry) All() []*Spec { return r.all }

// Get returns the format called name, or nil.
func (r *Registry) Get(name string) *Spec { return r.byName[name] }

// Hooks returns the asset hooks of format name, or nil (assets.Pipeline.SetHooks).
func (r *Registry) Hooks(name string) *assets.Hooks { return r.hooks[name] }

// RebuildRun returns the Rebuild.Run of format name, or nil (rebuild.New).
func (r *Registry) RebuildRun(name string) rebuild.Run {
	if s := r.byName[name]; s != nil && s.Rebuild != nil {
		return s.Rebuild.Run
	}
	return nil
}

// OnRepoChange runs the OnRepoChange of the format of new (repos.Service.SetChangeHook).
func (r *Registry) OnRepoChange(ctx context.Context, tx *sql.Tx, old, new *repos.Repo) error {
	if s := r.byName[new.Format]; s != nil && s.OnRepoChange != nil {
		return s.OnRepoChange(ctx, tx, old, new)
	}
	return nil
}

// Names returns the format names in module order.
func (r *Registry) Names() []string {
	out := make([]string, len(r.all))
	for i, s := range r.all {
		out[i] = s.Name
	}
	return out
}

func (r *Registry) unknown(name string) error {
	return fmt.Errorf("unknown format %q; available formats: %s", name, strings.Join(r.Names(), ", "))
}

// SupportsKind returns an error when format name takes no repos of kind (repos.KindCheck).
func (r *Registry) SupportsKind(name, kind string) error {
	s := r.Get(name)
	if s == nil {
		return r.unknown(name)
	}
	if !s.Takes(kind) {
		return fmt.Errorf("%s repositories cannot be a %s", s.Title, kind)
	}
	return nil
}

// ValidateConfig validates the name repoName and the config raw of a repo of format name
// (repos.Validator). An empty raw or null selects the format's default config.
func (r *Registry) ValidateConfig(name, repoName string, raw json.RawMessage) (json.RawMessage, error) {
	s := r.Get(name)
	if s == nil {
		return nil, r.unknown(name)
	}
	if s.ValidName != nil {
		if err := s.ValidName(repoName); err != nil {
			return nil, err
		}
	}
	if t := bytes.TrimSpace(raw); len(t) == 0 || string(t) == "null" {
		raw = s.Config.Default
	}
	return s.Config.Validate(raw)
}

// Importer returns the format whose importer accepts files of sourceFormat, or nil.
func (r *Registry) Importer(sourceFormat string) *Spec {
	for _, s := range r.all {
		if s.Importer != nil && slices.Contains(s.Importer.Sources, sourceFormat) {
			return s
		}
	}
	return nil
}
