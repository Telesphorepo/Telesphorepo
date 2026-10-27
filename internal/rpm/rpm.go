// Package rpm is the RPM (dnf) module: package uploads, signed by the server inside each file, and
// the signed repodata that the kernel's rebuild queue builds again after each change.
package rpm

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/rebuild"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/rpm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// name is the module ID and the format name: URL segment, repos.format value and schema
// component.
const name = "rpm"

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed queries/*.sql
var queriesFS embed.FS

type Handler struct {
	db       *db.DB
	q        *dbq.Queries // on the reader pool
	assets   *assets.Pipeline
	reader   *assets.Reader
	rebuilds *rebuild.Queue
	settings *settings.Service
	log      *slog.Logger
	keys     *signing.Keys
	host     *module.Host
}

// Module returns the RPM module: the format rpm with proxies, uploads, the signed repodata that the
// rebuild queue builds, the Nexus yum import, demo data and seeds.
func Module() module.Module {
	h := &Handler{}
	return module.Module{
		ID: name, Title: "RPM (dnf)", Requires: []string{"signing"},
		Register: func(x *module.Extender) {
			x.Schema(db.Component{
				Migrations: db.Sub(migrationsFS, "migrations"),
				Queries:    db.Sub(queriesFS, "queries"),
				Extra:      dbq.Extra,
				Allow: map[string]string{
					"VersionsPage": "the scans are of its own CTE (the keys of one page, at most its limit); every table read is a seek of rpm_packages_order or a primary key",
				},
			})
			x.Init(func(ctx context.Context, host *module.Host) error {
				h.host = host
				h.db, h.q = host.DB, dbq.New(host.DB.R)
				host.Repos.OnPurge(purge(dbq.New(host.DB.W)))
				h.assets, h.reader, h.rebuilds = host.Assets, host.Reader, host.Rebuilds
				h.settings, h.log = host.Settings, host.Log
				h.keys = module.Use[*signing.Keys](host)
				return nil
			})
			x.Routes(h.apiRoutes)
			x.Format(format.Spec{
				Name: name, Title: "RPM (dnf)", Icon: icon,
				Description:   "Packages that dnf installs on Fedora, RHEL, Rocky, Alma and CentOS Stream.",
				Kinds:         []string{repos.KindHosted, repos.KindProxy},
				Config:        format.ConfigSpec{Default: json.RawMessage(`{}`), Validate: ValidateConfig},
				SigningKey:    true,
				RepoTabs:      []string{"signing"},
				Routes:        h.routes,
				Describe:      describe,
				Admit:         h.admit,
				OnCommitted:   h.onCommitted,
				OnRemoved:     h.onRemoved,
				OnRepoChange:  h.onRepoChange,
				OnRepoCreate:  h.onRepoCreate,
				Classify:      classify,
				ProxyProbe:    func(json.RawMessage) string { return "" },
				Rebuild:       &format.Rebuild{Run: h.rebuild},
				Versions:      h.Versions,
				Builds:        h.Builds,
				Usage:         h.Usage,
				ArtifactUsage: h.ArtifactUsage,
				Tools:         []format.Tool{format.CLITool("anchor-1.0-1.x86_64.rpm")},
				Publish:       format.PublishCLI,
				Upload:        h.uploadSpec(),
				Importer:      h.importer(),
				UI:            ui,
				Edge: &format.EdgeSpec{Shape: format.EdgeStandard,
					Immutable: []string{"**/*.rpm", "repodata/*"},
					Mutable:   []string{"repodata/repomd.xml", "repodata/repomd.xml.asc"}},
			})
			x.Demo(module.DemoData{Repos: demoRepos, Fill: h.Demo})
			x.Seed(seed)
		},
	}
}

// routes mounts PUT and POST /rpm/{repo}/upload, GET /rpm/{repo}/key.asc (the repo's public
// signing key) and GET /rpm/{repo}/{path...} (the repodata and the packages, through the read
// path). The stored path of a file is its URL path: dnf reads each package's path from the
// repodata.
func (h *Handler) routes(r *route.Router, k format.Kit) {
	r.Handle("PUT /rpm/{repo}/upload", route.FormatWrite(name), h.upload)
	r.Handle("POST /rpm/{repo}/upload", route.FormatWrite(name), h.upload)
	r.Handle("GET /rpm/{repo}/key.asc", route.FormatRead(name), h.publicKey)
	r.Handle("GET /rpm/{repo}/{path...}", route.FormatRead(name), h.get)
}

// get serves a file of the repo.
func (h *Handler) get(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, err := httpx.RepoPath(r, "path")
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid path")
		return
	}
	h.reader.Serve(w, r, c, p)
}

const (
	rpmContentType = "application/x-rpm"
	sigContentType = "application/pgp-signature"
)

// describe gives the content type and the kind of a path: packages are artifacts, the repodata
// and every other file of a proxy's remote are indexes.
func describe(p repopath.Path) (assets.Describe, bool) {
	s := string(p)
	switch {
	case strings.HasSuffix(s, ".rpm"):
		return assets.Describe{ContentType: rpmContentType, Kind: assets.KindArtifact}, true
	case strings.HasSuffix(s, ".asc"):
		return assets.Describe{ContentType: sigContentType, Kind: assets.KindIndex}, true
	case strings.HasSuffix(s, ".xml"):
		return assets.Describe{ContentType: "application/xml", Kind: assets.KindIndex}, true
	case strings.HasSuffix(s, ".gz"):
		return assets.Describe{ContentType: "application/gzip", Kind: assets.KindIndex}, true
	}
	return assets.Describe{ContentType: assets.FallbackContentType, Kind: assets.KindIndex}, true
}

// hashNamed is a repodata file whose name starts with its checksum (createrepo's unique names).
var hashNamed = regexp.MustCompile(`(^|/)repodata/[0-9a-f]{32,128}-[^/]+$`)

// classify tells the proxy how to cache a path of a remote's layout (e.g.
// releases/44/Everything/x86_64/os/Packages/a/a-1-1.x86_64.rpm): packages and repodata files
// named by their checksum never change; repomd.xml, its signature and every other file change.
func classify(_ *repos.Repo, _ *repos.Proxy, p repopath.Path) (assets.Class, bool) {
	s := string(p)
	if strings.HasSuffix(s, ".rpm") || hashNamed.MatchString(s) {
		return assets.Class{}, true
	}
	return assets.Class{Changing: true}, true
}

// publicKey serves the armored public key of the repo's signing key, with the read gate of the
// repo. A proxy has no key of its own: clients use the keys of the remote's packagers.
func (h *Handler) publicKey(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	if repo.Kind == repos.KindProxy {
		httpx.Error(w, r, http.StatusNotFound, "a proxy repository has no signing key; use the keys of the remote's packagers")
		return
	}
	k, ok := h.keys.Get(signingKey(repo))
	if !ok {
		httpx.Error(w, r, http.StatusNotFound, "the signing key of this repository does not exist")
		return
	}
	key := []byte(k.PublicKey)
	w.Header().Set("Content-Type", "application/pgp-keys")
	w.Header().Set("Content-Length", fmt.Sprint(len(key)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(key)
	}
}

// The units of the rebuild queue: the group groupRepodata builds the repodata (unitIndex); the
// group groupSign signs every package again (unitSign), then marks the repodata.
const (
	groupRepodata = "repodata"
	unitIndex     = "index"
	groupSign     = "sign"
	unitSign      = "packages"
)

// onRepoCreate marks the repodata of a new hosted repo, so dnf reads an empty repo rather than a
// 404 before the first upload (dnf fails every command on a repo it cannot load).
func (h *Handler) onRepoCreate(ctx context.Context, tx *sql.Tx, repo *repos.Repo) error {
	if repo.Kind != repos.KindHosted {
		return nil
	}
	return h.rebuilds.MarkTx(ctx, tx, repo.ID, groupRepodata, unitIndex)
}

// onRepoChange runs in the transaction of a repo update (format.Spec OnRepoChange): a change of
// the signing key signs every package again, then the repodata.
func (h *Handler) onRepoChange(ctx context.Context, tx *sql.Tx, old, new *repos.Repo) error {
	if new.Kind != repos.KindHosted || signingKey(old) == signingKey(new) {
		return nil
	}
	return h.rebuilds.MarkTx(ctx, tx, new.ID, groupSign, unitSign)
}
