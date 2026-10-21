// Package pypi serves Python package repos at /pypi/{repo}/: hosted repos that twine, uv publish,
// poetry publish and telesphorepo publish upload to through the legacy upload API, proxies of
// pypi.org (or another index) and groups of both. pip, uv and poetry install through the simple
// repository API (PEP 503, PEP 691, PEP 658, PEP 700), which the server renders per request: a
// hosted repo from its rows, a proxy from the upstream page it caches, a group from the first
// member that has the project.
package pypi

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/proxy"
	"github.com/Telesphorepo/Telesphorepo/internal/pypi/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// name is the module ID and the format name: URL segment, repos.format value and schema
// component.
const name = "pypi"

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed queries/*.sql
var queriesFS embed.FS

// Handler serves the PyPI format.
type Handler struct {
	host   *module.Host
	q      *dbq.Queries // on the reader pool
	assets *assets.Pipeline
	log    *slog.Logger
	docs   *format.DocumentCache // rendered project pages: hosted ones by revision, proxy ones by upstream page
}

// Module returns the PyPI module: the format with proxies, groups, the legacy upload, web UI
// uploads, imports, demo data and seeds.
func Module() module.Module {
	h := &Handler{docs: format.NewDocumentCache(1024, 64<<20, 8<<20)}
	return module.Module{
		ID: name, Title: "Python (PyPI)",
		Register: func(x *module.Extender) {
			x.Schema(db.Component{Migrations: db.Sub(migrationsFS, "migrations"), Queries: db.Sub(queriesFS, "queries")})
			x.Init(func(ctx context.Context, host *module.Host) error {
				h.host, h.q, h.assets, h.log = host, dbq.New(host.DB.R), host.Assets, host.Log
				host.Repos.OnPurge(purge(dbq.New(host.DB.W))) // empties the module's rows of a repo in batches before the repo row goes
				return nil
			})
			x.Format(format.Spec{
				Name: name, Title: "Python (PyPI)", Icon: icon,
				Description:   "Python packages that pip, uv and poetry install.",
				Kinds:         []string{repos.KindHosted, repos.KindProxy, repos.KindGroup},
				Config:        format.ConfigSpec{Default: json.RawMessage(`{}`), Fields: configFields, Validate: validateConfig},
				Routes:        h.routes,
				Describe:      describe,
				Admit:         admit,
				OnCommitted:   h.onCommitted,
				OnRemoved:     h.onRemoved,
				Classify:      classify,
				ProxyProbe:    func(json.RawMessage) string { return "" },
				Versions:      h.Versions,
				Builds:        h.Builds,
				GroupVersions: format.GroupOwner,
				Usage:         h.Usage,
				ArtifactUsage: h.ArtifactUsage,
				Tools:         tools(),
				Publish:       format.PublishCLI,
				Upload:        h.uploadSpec(),
				Importer:      &format.Importer{Sources: []string{"pypi"}, Target: importTarget, Store: h.importStore},
				UI:            ui,
				Edge:          &format.EdgeSpec{Shape: format.EdgeStandard, Immutable: []string{"packages/**/*", "files/**/*", "remote/**/*"}},
			})
			x.Demo(module.DemoData{Repos: demoRepos, Fill: h.Demo})
			x.Seed(seed)
		},
	}
}

// defaultFiles is the files base of a proxy whose config names none: the file host of pypi.org.
const defaultFiles = "https://files.pythonhosted.org/"

// config is the repo config of a PyPI repo.
type config struct {
	// Files is the base URL that a proxy's remote serves its package files from ("" for
	// defaultFiles). Hosted repos and groups do not use it.
	Files string `json:"files,omitempty"`
}

var configFields = []format.Field{{Key: "files", Label: "Files address", Type: format.FieldString,
	Placeholder: defaultFiles, Default: json.RawMessage(`"` + defaultFiles + `"`),
	Help: "For a proxy: the address the remote index serves its package files from, besides its own. Empty: " + defaultFiles +
		" (pypi.org). A file at another address is left out of the pages. Hosted repositories and groups do not use it."}}

// validateConfig accepts {"files": "<http(s) URL>"}; files may be missing or empty.
func validateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var c config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid pypi config: %w", err)
	}
	if c.Files = strings.TrimSpace(c.Files); c.Files != "" {
		u, err := proxy.FilesURL(c.Files)
		if err != nil {
			return nil, fmt.Errorf("files: %w", err)
		}
		c.Files = u
	}
	return json.Marshal(c)
}

// filesBase returns the files base of repo: its config's files, else defaultFiles.
func filesBase(repo *repos.Repo) string {
	var c config
	if json.Unmarshal(repo.Config, &c) == nil && c.Files != "" {
		return c.Files
	}
	return defaultFiles
}

// Content types.
const (
	ctJSON     = "application/vnd.pypi.simple.v1+json"
	ctHTML     = "application/vnd.pypi.simple.v1+html"
	ctTextHTML = "text/html; charset=utf-8"
)

// describe gives a PyPI path its content type and kind: the distribution files under packages/
// (hosted), files/ and remote/ (proxy), their .metadata files, and the cached upstream pages under
// .simple/. Every other path is refused.
func describe(p repopath.Path) (assets.Describe, bool) {
	s := string(p)
	switch {
	case strings.HasPrefix(s, ".simple/"):
		return assets.Describe{ContentType: ctJSON, Kind: assets.KindIndex}, true
	case !strings.HasPrefix(s, "packages/") && !strings.HasPrefix(s, "files/") && !strings.HasPrefix(s, "remote/"):
		return assets.Describe{}, false
	case strings.HasSuffix(s, ".metadata"):
		return assets.Describe{ContentType: "text/plain; charset=utf-8", Kind: assets.KindMetadata}, true
	}
	ct := assets.FallbackContentType
	switch extOf(p.Base()) {
	case "whl", "zip":
		ct = "application/zip"
	case "tar.gz":
		ct = "application/gzip"
	}
	return assets.Describe{ContentType: ct, Kind: assets.KindArtifact}, true
}

// distMeta is the Meta of the write of a distribution file through ingest.
type distMeta struct {
	project, display, filename string
	md                         *coreMetadata
	metaSHA                    string // the SHA-256 of the .metadata sibling; "" for an sdist
}

// sidecar is the Meta of the write of a .metadata sibling through ingest.
type sidecar struct{}

// errExists is the message of a file name that holds other bytes. twine --skip-existing and
// uv publish read "File already exists" (and the status 409).
const errExists = "File already exists"

// admit decides a write. A distribution file and its .metadata come only through ingest: the
// same bytes are kept, other bytes at a file name that exists answer 409. A proxy stores and
// replaces what its remote sends. Every other write (a core upload of a path) is refused.
func admit(_ context.Context, w *assets.Write, existing *assets.Asset) (assets.Decision, error) {
	same := existing != nil && existing.SHA256 == w.Body.Digest()
	switch w.Meta.(type) {
	case *distMeta:
		switch {
		case existing == nil:
			return assets.Store, nil
		case same:
			return assets.Keep, nil
		}
		return 0, assets.Refuse(http.StatusConflict, "%s: %s with other content", errExists, w.Path.Base())
	case sidecar:
		if same {
			return assets.Keep, nil
		}
		return assets.Store, nil
	}
	if w.Origin.Source == assets.SourceProxy {
		if same {
			return assets.Keep, nil
		}
		return assets.Store, nil
	}
	return 0, assets.Refuse(http.StatusBadRequest,
		"Python packages are uploaded through /pypi/{repo}/legacy/ (twine, uv publish, poetry publish or telesphorepo publish) or the web UI")
}

// onCommitted writes the project and the file row of a stored distribution file, in the
// transaction of the write.
func (h *Handler) onCommitted(ctx context.Context, tx *sql.Tx, w *assets.Write, a *assets.Asset, _ *assets.Asset) error {
	m, ok := w.Meta.(*distMeta)
	if !ok {
		return nil
	}
	q := dbq.New(tx)
	if err := q.UpsertProject(ctx, dbq.UpsertProjectParams{RepoID: w.Repo.ID, Project: m.project, DisplayName: m.display, Rev: time.Now().UnixNano()}); err != nil {
		return err
	}
	return q.InsertFile(ctx, dbq.InsertFileParams{RepoID: w.Repo.ID, Project: m.project, Version: m.md.version.String(),
		SortKey: m.md.version.SortKey(), Filename: m.filename, Path: a.Path, AssetID: a.ID, Sha256: a.SHA256, Size: a.Size,
		RequiresPython: m.md.requiresPython, MetadataSha256: m.metaSHA, UploadedAt: a.CreatedAt.Unix()})
}

// onRemoved runs in the transaction of a remove: it deletes the file rows of removed distribution
// files (their .metadata follows), stops advertising a removed .metadata, deletes a project
// without files and moves the revision of a project with files left on. A remove of the whole repo does not call it.
func (h *Handler) onRemoved(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*assets.Asset, _ []repopath.Path) ([]repopath.Path, error) {
	q := dbq.New(tx)
	var follow []repopath.Path
	projects := map[string]bool{}
	for _, a := range removed {
		if !strings.HasPrefix(a.Path, "packages/") {
			continue
		}
		if base, ok := strings.CutSuffix(a.Path, ".metadata"); ok {
			ps, err := q.ClearMetadata(ctx, dbq.ClearMetadataParams{RepoID: repo.ID, Filename: repopath.Path(base).Base()})
			if err != nil {
				return nil, err
			}
			for _, p := range ps {
				projects[p] = true
			}
			continue
		}
		ps, err := q.DeleteFilesOfAsset(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			projects[p] = true
		}
		if len(ps) > 0 {
			follow = append(follow, repopath.Path(a.Path+".metadata"))
		}
	}
	for p := range projects {
		has, err := q.ProjectHasFiles(ctx, dbq.ProjectHasFilesParams{RepoID: repo.ID, Project: p})
		if err != nil {
			return nil, err
		}
		if !has {
			err = q.DeleteProject(ctx, dbq.DeleteProjectParams{RepoID: repo.ID, Project: p})
		} else {
			err = q.BumpProject(ctx, dbq.BumpProjectParams{At: time.Now().UnixNano(), RepoID: repo.ID, Project: p})
		}
		if err != nil {
			return nil, err
		}
	}
	return follow, nil
}

// pageAccept is the Accept of a fetch of an upstream project page: the JSON form, else HTML.
const pageAccept = ctJSON + ", " + ctHTML + ";q=0.2, text/html;q=0.01"

// classify tells the proxy how to cache a path: the cached upstream page .simple/<name> changes;
// the package files under files/ and remote/ (and their .metadata) never do. A files/ path comes
// from the files base of the repo config, a remote/ path from the remote URL.
func classify(repo *repos.Repo, _ *repos.Proxy, p repopath.Path) (assets.Class, bool) {
	s := string(p)
	switch {
	case strings.HasPrefix(s, ".simple/"):
		return assets.Class{Changing: true, Remote: "simple/" + strings.TrimPrefix(s, ".simple/") + "/",
			Header: http.Header{"Accept": {pageAccept}}, KeepType: true}, true
	case strings.HasPrefix(s, "files/"):
		return assets.Class{Remote: strings.TrimPrefix(s, "files/"), Files: filesBase(repo)}, true
	case strings.HasPrefix(s, "remote/"):
		return assets.Class{Remote: strings.TrimPrefix(s, "remote/")}, true
	}
	return assets.Class{}, false
}

// routes mounts the simple API, the files and the legacy upload.
func (h *Handler) routes(r *route.Router, _ format.Kit) {
	read := route.FormatRead(name)
	write := route.FormatWrite(name)
	write.Errors = func(w http.ResponseWriter, r *http.Request, d access.Decision) { fail(w, r, d.Status, d.Message) }
	r.Handle("GET /pypi/{repo}/simple/{$}", read, h.index)
	r.Handle("GET /pypi/{repo}/simple", read, h.toIndex)
	r.Handle("GET /pypi/{repo}/simple/{project}/{$}", read, h.page)
	r.Handle("GET /pypi/{repo}/simple/{project}", read, h.page)
	r.Handle("GET /pypi/{repo}/packages/{path...}", read, h.file("packages/"))
	r.Handle("GET /pypi/{repo}/files/{path...}", read, h.file("files/"))
	r.Handle("GET /pypi/{repo}/remote/{path...}", read, h.file("remote/"))
	r.Handle("POST /pypi/{repo}/{$}", write, h.upload)
	r.Handle("POST /pypi/{repo}/legacy/{$}", write, h.upload)
	r.Handle("POST /pypi/{repo}/legacy", write, h.upload)
}

// ui is what the web UI shows of PyPI repos.
var ui = format.UIDescriptor{
	DeleteNotes: []format.DeleteNote{{Match: "packages/*/*.whl", One: "wheel", Many: "wheels",
		Text: "The project page stops listing the deleted files; a version without files goes."}},
	RemotePresets:  []format.RemotePreset{{Label: "PyPI", URL: "https://pypi.org/"}},
	ProxyNote:      "The pages list the files of the remote index with their hashes; pip and uv check each download against them.",
	ArtifactKinds:  map[string]string{"project": "Project"},
	ArtifactName:   map[string]string{"project": "Project"},
	SnippetTools:   map[string]string{"pip": "pip", "uv": "uv", "poetry": "Poetry", "telesphorepo": "Telesphorepo CLI"},
	InstallHeading: "Install it",
	BuildFiles: []format.BuildFileLabel{
		{Extension: "whl", Classifier: "*", Label: "Wheel ({classifier})"},
		{Extension: "tar.gz", Label: "Source distribution"},
		{Extension: "zip", Label: "Source distribution (zip)"},
	},
}
