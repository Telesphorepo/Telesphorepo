// Package nuget serves NuGet repos at /nuget/{repo}/: the NuGet V3 protocol that dotnet, nuget.exe
// and Visual Studio speak. Packages (.nupkg) and their .nuspec are assets of the byte pipeline at
// the path of their flat-container URL; the service index, the version lists, the registrations
// and the search are rendered per request: from the module's rows in a hosted repo, from the
// cached documents of the remote in a proxy, and from the member that owns the package name in a
// group.
package nuget

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/nuget/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// name is the module ID and the format name: URL segment, repos.format value and schema
// component.
const name = "nuget"

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed queries/*.sql
var queriesFS embed.FS

// Audit actions of the module.
const (
	ActionUnlist = "nuget.unlist"
	ActionRelist = "nuget.relist"
)

// apiKeyHeader carries the publish session on a push, an unlist and a relist.
const apiKeyHeader = "X-NuGet-ApiKey"

// Handler serves the NuGet format.
type Handler struct {
	host *module.Host
	q    *dbq.Queries // on the reader pool
	// docs keeps rendered documents, keyed by the revision of a hosted package or the SHA-256s of
	// the remote documents of a proxy, and the base URL.
	docs *format.DocumentCache
	// upstreams holds the resources of the service index of each proxy remote, by remote URL,
	// with the SHA-256 of the copy they come from (see upstream.go).
	upstreams sync.Map
}

// Module returns the NuGet module: the NuGet format with proxies, groups, uploads, imports, demo
// data and seeds. The Telesphorepo CLI publishes to it ("telesphorepo publish").
func Module() module.Module {
	h := &Handler{docs: format.NewDocumentCache(1024, 64<<20, 8<<20)}
	return module.Module{
		ID: name, Title: "NuGet",
		Register: func(x *module.Extender) {
			x.Schema(db.Component{Migrations: db.Sub(migrationsFS, "migrations"), Queries: db.Sub(queriesFS, "queries"), Extra: dbq.Extra})
			x.AuditActions(
				audit.ActionDef{ID: ActionUnlist, Label: "NuGet package unlisted", Subsystem: "repo"},
				audit.ActionDef{ID: ActionRelist, Label: "NuGet package relisted", Subsystem: "repo"},
			)
			x.Init(func(ctx context.Context, host *module.Host) error {
				h.host, h.q = host, dbq.New(host.DB.R)
				host.Repos.OnPurge(purge(dbq.New(host.DB.W))) // empties the module's rows of a repo in batches before the repo row goes
				return nil
			})
			x.Format(format.Spec{
				Name: name, Title: "NuGet", Icon: icon,
				Description:   ".NET packages that dotnet, NuGet and Visual Studio install.",
				Kinds:         []string{repos.KindHosted, repos.KindProxy, repos.KindGroup},
				Config:        format.ConfigSpec{Default: json.RawMessage(`{}`), Validate: validateConfig},
				Routes:        h.routes,
				Describe:      describe,
				Admit:         admit,
				OnCommitted:   h.onCommitted,
				OnRemoved:     h.onRemoved,
				Classify:      h.classify,
				ProxyProbe:    func(json.RawMessage) string { return "" },
				Versions:      h.Versions,
				Builds:        h.Builds,
				GroupVersions: format.GroupOwner,
				Usage:         h.Usage,
				ArtifactUsage: h.ArtifactUsage,
				Tools:         tools(),
				Publish:       format.PublishCLI,
				Upload:        h.uploadSpec(),
				Importer:      &format.Importer{Sources: []string{"nuget"}, Target: importTarget, Store: h.importStore},
				UI:            ui,
				Edge: &format.EdgeSpec{Shape: format.EdgeStandard,
					Immutable: []string{flatDir + "*/*/*.nupkg", flatDir + "*/*/*.nuspec"}},
			})
			x.Demo(module.DemoData{Repos: demoRepos, Fill: h.Demo})
			x.Seed(seed)
		},
	}
}

// validateConfig accepts only an empty JSON object: NuGet repos have no options.
func validateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, errors.New("nuget config must be a JSON object")
	}
	if len(m) > 0 {
		return nil, errors.New("nuget repos take no config options")
	}
	return json.RawMessage(`{}`), nil
}

// ui is what the web UI shows of NuGet repos.
var ui = format.UIDescriptor{
	DeleteNotes: []format.DeleteNote{{Match: flatDir + "*/*/*.nupkg", One: "version", Many: "versions",
		Text: "The package versions leave the version lists, the registration and the search."}},
	RemotePresets:  []format.RemotePreset{{Label: "nuget.org", URL: "https://api.nuget.org/v3/index.json"}},
	ProxyNote:      "Packages keep the signatures of their authors and of nuget.org: the server stores their bytes unchanged.",
	ArtifactKinds:  map[string]string{"package": "Package"},
	ArtifactName:   map[string]string{"package": "Package ID"},
	SnippetTools:   map[string]string{"dotnet": ".NET CLI", "nuget-config": "nuget.config", "msbuild": "Project file", "telesphorepo": "Telesphorepo CLI"},
	InstallHeading: "Install it",
	BuildFiles: []format.BuildFileLabel{
		{Extension: "nupkg", Label: "Package (.nupkg)"},
		{Extension: "nuspec", Label: "Package manifest (.nuspec)"},
	},
}

// flatDir is the folder of the packages: the flat container (PackageBaseAddress) of the repo.
const flatDir = "v3-flatcontainer/"

// upstreamDir holds the documents that a proxy caches from its remote: paths that no client asks
// for (see upstream.go).
const upstreamDir = ".upstream/"

// packagePaths returns the paths of the .nupkg and the .nuspec of a package version: lowered id
// and lowered normalised version.
func packagePaths(lid, lver string) (nupkg, nuspec string) {
	dir := flatDir + lid + "/" + lver + "/"
	return dir + lid + "." + lver + ".nupkg", dir + lid + ".nuspec"
}

// flatFile splits a path of the flat container into the lowered id, the version folder and the
// file name; ok is false for another path.
func flatFile(p string) (lid, lver, file string, ok bool) {
	rest, found := strings.CutPrefix(p, flatDir)
	if !found {
		return "", "", "", false
	}
	segs := strings.Split(rest, "/")
	if len(segs) != 3 || segs[0] == "" || segs[1] == "" || segs[2] == "" {
		return "", "", "", false
	}
	return segs[0], segs[1], segs[2], true
}

// describe gives the content type and the kind of a NuGet path: a .nupkg is an artifact, a
// .nuspec its metadata, a cached remote document JSON metadata. Every other path is refused.
func describe(p repopath.Path) (assets.Describe, bool) {
	s := string(p)
	if strings.HasPrefix(s, upstreamDir) {
		return assets.Describe{ContentType: "application/json", Kind: assets.KindMetadata}, true
	}
	_, _, file, ok := flatFile(s)
	switch {
	case !ok:
		return assets.Describe{}, false
	case strings.HasSuffix(file, ".nupkg"):
		return assets.Describe{ContentType: assets.FallbackContentType, Kind: assets.KindArtifact}, true
	case strings.HasSuffix(file, ".nuspec"):
		return assets.Describe{ContentType: "application/xml", Kind: assets.KindMetadata}, true
	}
	return assets.Describe{}, false
}

// pushMeta is the Meta of the writes of a pushed package: the package, and whether the write is
// its .nuspec.
type pushMeta struct {
	info   *pkgInfo
	nuspec bool
}

// admit decides a write. A package version is immutable: its files keep the same bytes, and other
// bytes are refused with 409. A proxy stores what its remote sends. Every other write without a
// parsed package (a core upload session) is refused (400): packages are pushed.
func admit(_ context.Context, w *assets.Write, existing *assets.Asset) (assets.Decision, error) {
	m, ok := w.Meta.(*pushMeta)
	if !ok {
		if w.Origin.Source != assets.SourceProxy {
			return 0, assets.Refuse(http.StatusBadRequest, "NuGet packages are pushed to /nuget/{repo}/package (dotnet nuget push)")
		}
		if existing != nil && existing.SHA256 == w.Body.Digest() {
			return assets.Keep, nil
		}
		return assets.Store, nil
	}
	switch {
	case existing == nil:
		return assets.Store, nil
	case existing.SHA256 == w.Body.Digest():
		return assets.Keep, nil
	}
	return 0, assets.Refuse(http.StatusConflict, "package %s %s already exists with other content", m.info.ID, m.info.Version.Normalized())
}

// routes mounts the NuGet V3 resources of /nuget/{repo}/. Reads take the read gate; push,
// unlist and relist need a publish session, which dotnet sends as the API key.
func (h *Handler) routes(r *route.Router, _ format.Kit) {
	read := route.FormatRead(name)
	write := route.FormatWrite(name)
	write.TokenHeader = apiKeyHeader
	r.Handle("GET /nuget/{repo}/index.json", read, h.serviceIndex)
	r.Handle("GET /nuget/{repo}/vulnerabilities/index.json", read, h.vulnerabilityIndex)
	r.Handle("GET /nuget/{repo}/vulnerabilities/base.json", read, h.vulnerabilityPage)
	r.Handle("GET /nuget/{repo}/v3-flatcontainer/{id}/index.json", read, h.flatIndex)
	r.Handle("GET /nuget/{repo}/v3-flatcontainer/{id}/{ver}/{file}", read, h.flatFile)
	for _, base := range []string{regSemVer2, regSemVer1} {
		r.Handle("GET /nuget/{repo}/"+base+"/{id}/index.json", read, h.registrationIndex)
		r.Handle("GET /nuget/{repo}/"+base+"/{id}/page/{lower}/{upper}", read, h.registrationPage)
		r.Handle("GET /nuget/{repo}/"+base+"/{id}/{leaf}", read, h.registrationLeaf)
	}
	r.Handle("GET /nuget/{repo}/query", read, h.search)
	r.Handle("PUT /nuget/{repo}/package", write, h.push)
	r.Handle("PUT /nuget/{repo}/package/{$}", write, h.push)
	r.Handle("DELETE /nuget/{repo}/package/{id}/{ver}", write, h.unlist)
	r.Handle("POST /nuget/{repo}/package/{id}/{ver}", write, h.relist)
}
