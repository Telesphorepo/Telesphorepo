// Package npm serves npm registries at /npm/{repo}/: hosted repos that npm, pnpm, yarn and bun
// publish to, proxies of a remote registry (registry.npmjs.org) and groups of both. Tarballs are
// assets of the byte pipeline; packuments are rendered per request from the npm_* rows (hosted),
// from the cached remote packument (proxy) or from the member that owns the name (group).
package npm

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/npm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// name is the module ID and the format name: URL segment, repos.format value and schema
// component.
const name = "npm"

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed queries/*.sql
var queriesFS embed.FS

// Handler serves the npm format.
type Handler struct {
	db     *db.DB
	q      *dbq.Queries // on the reader pool
	assets *assets.Pipeline
	host   *module.Host
	log    *slog.Logger
	docs   *format.DocumentCache
}

// Module returns the npm module: the npm format with proxies, groups, uploads, imports, demo data
// and seeds. The Telesphorepo CLI publishes to it with the project's own package manager.
func Module() module.Module {
	// A full packument of a big remote package runs to tens of MB: an entry may take a quarter
	// of the cache, so it is rewritten once per change of the remote copy.
	h := &Handler{docs: format.NewDocumentCache(2000, 256<<20, 64<<20)}
	return module.Module{
		ID: name, Title: "npm",
		Register: func(x *module.Extender) {
			x.Schema(db.Component{Migrations: db.Sub(migrationsFS, "migrations"), Queries: db.Sub(queriesFS, "queries")})
			x.Init(func(ctx context.Context, host *module.Host) error {
				h.host, h.db, h.q = host, host.DB, dbq.New(host.DB.R)
				host.Repos.OnPurge(purge(dbq.New(host.DB.W))) // empties the module's rows of a repo in batches before the repo row goes
				h.assets, h.log = host.Assets, host.Log
				return nil
			})
			x.Format(format.Spec{
				Name: name, Title: "npm", Icon: icon,
				Description:   "JavaScript packages for npm, pnpm, yarn and bun.",
				Kinds:         []string{repos.KindHosted, repos.KindProxy, repos.KindGroup},
				Config:        format.ConfigSpec{Default: json.RawMessage(`{}`), Validate: validateConfig},
				Routes:        h.routes,
				Describe:      describe,
				Admit:         h.admit,
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
				Importer:      &format.Importer{Sources: []string{"npm"}, Target: importTarget, Store: h.importStore},
				Carry:         h.carry(),
				UI:            ui,
				Edge:          &format.EdgeSpec{Shape: format.EdgeStandard, Immutable: []string{"**/-/*.tgz"}},
			})
			x.Demo(module.DemoData{Repos: demoRepos, Fill: h.Demo})
			x.Seed(seed)
		},
	}
}

// validateConfig accepts only an empty JSON object: npm repos have no options.
func validateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, errors.New("npm config must be a JSON object")
	}
	if len(m) > 0 {
		return nil, errors.New("npm repos take no config options")
	}
	return json.RawMessage(`{}`), nil
}

// Cached remote packuments of a proxy: .packument/abbreviated/<name> and .packument/full/<name>,
// paths that no client asks for.
const (
	packumentDir = ".packument/"
	formFull     = "full"
	formAbbrev   = "abbreviated"
)

// packumentPath is the path of the cached remote packument of name in form.
func packumentPath(form, name string) string { return packumentDir + form + "/" + name }

// splitFile splits a file path <name>/-/<file> into the package name and the file; ok is false for
// another path.
func splitFile(p string) (pkg, file string, ok bool) {
	i := strings.LastIndex(p, "/-/")
	if i <= 0 {
		return "", "", false
	}
	pkg, file = p[:i], p[i+3:]
	if !validName(pkg) || file == "" || strings.Contains(file, "/") {
		return "", "", false
	}
	return pkg, file, true
}

// describe gives the content type and kind of an npm path: tarballs and provenance bundles under
// <name>/-/, and the cached remote packuments of a proxy. Every other path is refused.
func describe(p repopath.Path) (assets.Describe, bool) {
	s := string(p)
	if rest, ok := strings.CutPrefix(s, packumentDir); ok {
		form, pkg, _ := strings.Cut(rest, "/")
		if (form == formFull || form == formAbbrev) && validName(pkg) {
			return assets.Describe{ContentType: "application/json", Kind: assets.KindMetadata}, true
		}
		return assets.Describe{}, false
	}
	_, file, ok := splitFile(s)
	switch {
	case !ok:
		return assets.Describe{}, false
	case strings.HasSuffix(file, ".tgz"):
		return assets.Describe{ContentType: "application/octet-stream", Kind: assets.KindArtifact}, true
	case strings.HasSuffix(file, ".sigstore"):
		return assets.Describe{ContentType: "application/json", Kind: assets.KindMetadata}, true
	}
	return assets.Describe{}, false
}

// classify tells the proxy how to cache a path: a tarball never changes and lies at the same path
// at the remote; a cached packument changes and is the remote's document of the name, in the form
// that the Accept header asks for.
func classify(_ *repos.Repo, _ *repos.Proxy, p repopath.Path) (assets.Class, bool) {
	s := string(p)
	if rest, ok := strings.CutPrefix(s, packumentDir); ok {
		form, pkg, _ := strings.Cut(rest, "/")
		if !validName(pkg) {
			return assets.Class{}, false
		}
		switch form {
		case formAbbrev:
			return assets.Class{Changing: true, Remote: pkg, Header: map[string][]string{"Accept": {abbreviatedType}}}, true
		case formFull:
			return assets.Class{Changing: true, Remote: pkg, Header: map[string][]string{"Accept": {"application/json"}}}, true
		}
		return assets.Class{}, false
	}
	if _, file, ok := splitFile(s); ok && strings.HasSuffix(file, ".tgz") {
		return assets.Class{}, true
	}
	return assets.Class{}, false
}

// sidecarMeta is the Meta of the write of a provenance bundle next to a published tarball.
type sidecarMeta struct{}

// admit decides a write (format.Spec Admit). A version (publishMeta) that exists is refused: 403
// for a publish ("version 1.2.0 of x already exists"), and for an upload or an import the same
// tarball is kept and other bytes are refused with 409. A provenance bundle of a publish and a
// file a proxy fetched keep the same bytes and replace other ones. Every other write is refused:
// packages come through a publish, the upload or an import.
func (h *Handler) admit(ctx context.Context, w *assets.Write, existing *assets.Asset) (assets.Decision, error) {
	same := existing != nil && existing.SHA256 == w.Body.Digest()
	switch m := w.Meta.(type) {
	case *publishMeta:
		tb, err := h.q.GetVersionTarball(ctx, dbq.GetVersionTarballParams{RepoID: w.Repo.ID, Name: m.name, Version: m.version})
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return assets.Store, nil
		case err != nil:
			return 0, err
		case w.Origin.Source == assets.SourcePut:
			return 0, assets.Refuse(403, "version %s of %s already exists; publish a new version", m.version, m.name)
		case same && tb == string(w.Path):
			return assets.Keep, nil
		}
		return 0, assets.Refuse(409, "version %s of %s already exists with other content", m.version, m.name)
	case sidecarMeta:
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
	return 0, assets.Refuse(400, "npm packages are published with npm publish (or telesphorepo publish), or uploaded as a .tgz")
}

// ui is what the web UI shows of npm repos.
var ui = format.UIDescriptor{
	DeleteNotes: []format.DeleteNote{{Match: "**/-/*.tgz", One: "version", Many: "versions",
		Text: "the packument stops listing the deleted versions, and a dist-tag that named one goes."}},
	RemotePresets:  []format.RemotePreset{{Label: "npm registry", URL: "https://registry.npmjs.org/"}},
	ArtifactKinds:  map[string]string{"package": "Package"},
	ArtifactName:   map[string]string{"package": "Package"},
	SnippetTools:   map[string]string{"npm": "npm, pnpm, bun", "yarn": "Yarn", "telesphorepo": "Telesphorepo CLI"},
	InstallHeading: "Install it",
	BuildFiles:     []format.BuildFileLabel{{Extension: "tgz", Label: "Package tarball"}},
}
