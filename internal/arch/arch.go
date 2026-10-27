// Package arch is the Arch Linux (pacman) module: package uploads, and the signed indexes of each
// architecture that the kernel's rebuild queue builds again after each change.
package arch

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/arch/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/rebuild"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// name is the module ID and the format name: URL segment, repos.format value and schema
// component.
const name = "arch"

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

// Module returns the Arch Linux module: the format arch with proxies, uploads, the signed indexes
// that the rebuild queue builds, demo data and seeds.
func Module() module.Module {
	h := &Handler{}
	return module.Module{
		ID: name, Title: "Arch Linux (pacman)", Requires: []string{"signing"},
		Register: func(x *module.Extender) {
			x.Schema(db.Component{
				Migrations: db.Sub(migrationsFS, "migrations"),
				Queries:    db.Sub(queriesFS, "queries"),
				Extra:      dbq.Extra,
				Allow: map[string]string{
					"VersionsPage": "the scans are of its own CTE (the keys of one page, at most its limit); every table read is a seek of arch_packages_order or a primary key",
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
				Name: name, Title: "Arch Linux (pacman)", Icon: icon,
				Description:   "Packages that pacman installs on Arch Linux.",
				Kinds:         []string{repos.KindHosted, repos.KindProxy},
				Config:        format.ConfigSpec{Default: defaultConfig, Fields: configFields, Validate: ValidateConfig},
				SigningKey:    true,
				RepoTabs:      []string{"signing"},
				Routes:        h.routes,
				Describe:      describe,
				Admit:         h.admit,
				OnCommitted:   h.onCommitted,
				OnRemoved:     h.onRemoved,
				OnRepoChange:  h.onRepoChange,
				Classify:      classify,
				ProxyProbe:    func(json.RawMessage) string { return "" },
				Rebuild:       &format.Rebuild{Run: h.rebuild},
				Versions:      h.Versions,
				Builds:        h.Builds,
				Usage:         h.Usage,
				ArtifactUsage: h.ArtifactUsage,
				Tools:         []format.Tool{format.CLITool("anchor-1.0-1-x86_64.pkg.tar.zst")},
				Publish:       format.PublishCLI,
				Upload:        h.uploadSpec(),
				UI:            ui,
				Edge: &format.EdgeSpec{Shape: format.EdgePacman,
					Immutable: []string{"**/*.pkg.tar.*"}, Mutable: []string{"**/*.sig"}},
			})
			x.Demo(module.DemoData{Repos: demoRepos, Fill: h.Demo})
			x.Seed(seed)
		},
	}
}

// routes mounts PUT and POST /arch/{repo}/upload, GET /arch/{repo}/key.asc (the repo's public
// signing key) and GET /arch/{repo}/{path...} (the indexes and the packages, through the read
// path).
func (h *Handler) routes(r *route.Router, k format.Kit) {
	r.Handle("PUT /arch/{repo}/upload", route.FormatWrite(name), h.upload)
	r.Handle("POST /arch/{repo}/upload", route.FormatWrite(name), h.upload)
	r.Handle("GET /arch/{repo}/key.asc", route.FormatRead(name), h.publicKey)
	r.Handle("GET /arch/{repo}/{path...}", route.FormatRead(name), h.get)
}

// get serves a file of the repo. A hosted repo answers pacman's URLs under {arch}/ from where it
// keeps the files (ServedPath); a proxy serves the remote's paths as they are.
func (h *Handler) get(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, err := httpx.RepoPath(r, "path")
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid path")
		return
	}
	if c.Repo.Kind == repos.KindHosted {
		p = repopath.Path(ServedPath(r.PathValue("repo"), string(p)))
	}
	h.reader.Serve(w, r, c, p)
}

// ServedPath maps a path of a hosted repo's URL (after /arch/<seg>/) to the path that holds its
// file. A path of two segments <arch>/<file> is pacman's: <seg>.db, <seg>.db.sig, <seg>.files and
// <seg>.files.sig are the architecture's indexes (<arch>/index.db, ...), where <seg> is the repo
// name that the URL used (its own name or a redirect of it, which pacman calls the section); a
// package file <name>-<pkgver>-<pkgrel>-<parch>.pkg.tar.<ext>[.sig] of parch <arch> or any is
// pool/<name>/<file>. Every other path is itself. The edge Worker maps the same way
// (edge/src/shapes.ts, pacmanPath).
func ServedPath(seg, p string) string {
	a, f, ok := strings.Cut(p, "/")
	if !ok || strings.Contains(f, "/") || !archRE.MatchString(a) || a == "pool" {
		return p
	}
	for _, suffix := range []string{".db", ".db.sig", ".files", ".files.sig"} {
		if f == seg+suffix {
			return a + "/index" + suffix
		}
	}
	if n, parch, err := splitFileName(f); err == nil && (parch == a || parch == "any") {
		return "pool/" + n + "/" + f
	}
	return p
}

// describe gives the content type and the kind of a path: package files are artifacts, the
// indexes and the files of a proxy's remote that are not packages are indexes.
func describe(p repopath.Path) (assets.Describe, bool) {
	s := string(p)
	switch {
	case strings.HasSuffix(s, ".sig"):
		kind := assets.KindIndex
		if strings.Contains(s, ".pkg.tar.") {
			kind = assets.KindArtifact
		}
		return assets.Describe{ContentType: "application/pgp-signature", Kind: kind}, true
	case strings.Contains(baseName(s), ".pkg.tar."):
		return assets.Describe{ContentType: pkgContentType, Kind: assets.KindArtifact}, true
	}
	return assets.Describe{ContentType: assets.FallbackContentType, Kind: assets.KindIndex}, true
}

// baseName returns the last segment of s.
func baseName(s string) string { return s[strings.LastIndexByte(s, '/')+1:] }

// classify tells the proxy how to cache a path of a remote's layout (e.g.
// core/os/x86_64/core.db): package files and their signatures never change; the indexes and every
// other file change.
func classify(_ *repos.Repo, _ *repos.Proxy, p repopath.Path) (assets.Class, bool) {
	if strings.Contains(baseName(string(p)), ".pkg.tar.") {
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

// The units of the rebuild queue. The group of an index is its architecture; unitDB builds the
// index from arch_db_entries, unitFill writes the entries of the architecture again first (an
// architecture added to the config). The group groupSign signs every package again (unitSign).
const (
	unitDB    = "db"
	unitFill  = "fill"
	groupSign = "sign"
	unitSign  = "packages"
)

// onRepoChange runs in the transaction of a repo update (format.Spec OnRepoChange). An added
// architecture is filled and built; a removed one is marked, and its rebuild drops its entries and
// index files. A change of the signing key signs every package and index again.
func (h *Handler) onRepoChange(ctx context.Context, tx *sql.Tx, old, new *repos.Repo) error {
	if new.Kind != repos.KindHosted {
		return nil
	}
	oldArchs, newArchs := architectures(old), architectures(new)
	for _, a := range newArchs {
		if !slices.Contains(oldArchs, a) {
			if err := h.rebuilds.MarkTx(ctx, tx, new.ID, a, unitFill); err != nil {
				return err
			}
		}
	}
	for _, a := range oldArchs {
		if !slices.Contains(newArchs, a) {
			if err := h.rebuilds.MarkTx(ctx, tx, new.ID, a, unitDB); err != nil {
				return err
			}
		}
	}
	if signingKey(old) != signingKey(new) {
		return h.rebuilds.MarkTx(ctx, tx, new.ID, groupSign, unitSign)
	}
	return nil
}
