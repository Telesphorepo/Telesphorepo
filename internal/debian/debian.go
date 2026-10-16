// Package debian is the Debian (apt) module: uploads, and signed indexes that the
// kernel's rebuild queue builds again after each change.
package debian

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/debian/dbq"
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
const name = "debian"

//go:embed schema.sql
var schema string

//go:embed queries/*.sql
var queriesFS embed.FS

type Handler struct {
	db       *db.DB
	q        *dbq.Queries // on the reader pool
	assets   *assets.Pipeline
	rebuilds *rebuild.Queue
	repos    *repos.Service
	settings *settings.Service
	log      *slog.Logger

	keys *signing.Keys // the signing keys (live snapshot)

	host *module.Host
}

// Module returns the Debian module: the Debian format with proxies, uploads, imports, signed
// indexes that the rebuild queue builds, demo data and seeds. Configuration and the signing keys
// are read on every use, so changes apply live.
func Module() module.Module {
	h := &Handler{}
	return module.Module{
		ID: name, Title: "Debian (apt)", Requires: []string{"signing"},
		Register: func(x *module.Extender) {
			x.Schema(db.Component{
				Schema:  schema,
				Queries: db.Sub(queriesFS, "queries"),
				Extra:   dbq.Extra,
			})
			x.Init(func(ctx context.Context, host *module.Host) error {
				h.host = host
				h.db, h.q = host.DB, dbq.New(host.DB.R)
				h.assets, h.rebuilds, h.repos = host.Assets, host.Rebuilds, host.Repos
				h.settings, h.log = host.Settings, host.Log
				h.keys = module.Use[*signing.Keys](host)
				return nil
			})
			x.Format(format.Spec{
				Name: name, Title: "Debian (apt)", Icon: icon,
				Description:   "Packages that apt installs on Debian and Ubuntu.",
				Kinds:         []string{repos.KindHosted, repos.KindProxy},
				Config:        format.ConfigSpec{Default: defaultConfig, Fields: configFields, Validate: h.ValidateConfig},
				SigningKey:    true,
				RepoTabs:      []string{"signing"},
				Routes:        h.routes,
				Describe:      describe,
				Admit:         h.admit,
				OnCommitted:   h.onCommitted,
				OnRemoved:     h.onRemoved,
				OnRepoChange:  h.onRepoChange,
				Classify:      classify,
				ProxyProbe:    h.ProxyProbe,
				Rebuild:       &format.Rebuild{Run: h.rebuild},
				Versions:      h.Versions,
				Builds:        h.Builds,
				Usage:         h.Usage,
				ArtifactUsage: h.ArtifactUsage,
				Tools:         []format.Tool{format.CurlTool(curlUpload)},
				Upload:        h.uploadSpec(),
				Importer:      &format.Importer{Sources: []string{"apt"}, Target: h.importTarget, Store: h.importStore, Plan: h.importPlan},
				UI:            ui,
			})
			x.Demo(module.DemoData{Repos: demoRepos, Fill: h.Demo})
			x.Seed(seed)
		},
	}
}

// routes mounts /debian/{repo}/upload/{dist}/{component} (PUT, POST), GET/HEAD
// /debian/{repo}/key.asc (the repo's public signing key) and GET/HEAD /debian/{repo}/{path...}
// (dists/ and pool/, through the read path).
func (h *Handler) routes(r *route.Router, k format.Kit) {
	r.Handle("PUT /debian/{repo}/upload/{dist}/{component}", route.FormatWrite(name), h.upload)
	r.Handle("POST /debian/{repo}/upload/{dist}/{component}", route.FormatWrite(name), h.upload)
	r.Handle("GET /debian/{repo}/key.asc", route.FormatRead(name), h.publicKey)
	r.Handle("GET /debian/{repo}/{path...}", route.FormatRead(name), k.Get)
}

// describe gives the content type and the kind of a Debian path: the pool files are artifacts,
// the files under dists/ are indexes. Every other path is refused.
func describe(p repopath.Path) (assets.Describe, bool) {
	s := string(p)
	switch {
	case strings.HasPrefix(s, "pool/"):
		ct := assets.FallbackContentType
		if strings.HasSuffix(s, ".deb") {
			ct = debContentType
		}
		return assets.Describe{ContentType: ct, Kind: assets.KindArtifact}, true
	case strings.HasPrefix(s, "dists/"):
		d := assets.Describe{ContentType: assets.FallbackContentType, Kind: assets.KindIndex}
		switch base := p.Base(); {
		case strings.Contains(s, "/by-hash/"):
		case base == "Release" || base == "InRelease" || base == "Packages":
			d.ContentType = "text/plain"
		case strings.HasSuffix(base, ".gz"):
			d.ContentType = "application/gzip"
		case strings.HasSuffix(base, ".gpg"):
			d.ContentType = "application/pgp-signature"
		}
		return d, true
	}
	return assets.Describe{}, false
}

// classify tells the proxy how to cache a path: pool files and by-hash index files never change
// (a by-hash file's name is its checksum); the other files under dists/ (InRelease, Release,
// Packages, ...) change. Other paths are refused.
func classify(_ *repos.Proxy, p repopath.Path) (assets.Class, bool) {
	s := string(p)
	switch {
	case strings.HasPrefix(s, "pool/"):
		return assets.Class{}, true
	case strings.HasPrefix(s, "dists/") && strings.Contains(s, "/by-hash/"):
		return assets.Class{}, true
	case strings.HasPrefix(s, "dists/"):
		return assets.Class{Changing: true}, true
	}
	return assets.Class{}, false
}

// ProxyProbe is the path a check of the remote requests: the InRelease of the repo config's
// distribution ("stable" when it names none).
func (h *Handler) ProxyProbe(config json.RawMessage) string {
	var c struct {
		Distribution string `json:"distribution"`
	}
	_ = json.Unmarshal(config, &c)
	if c.Distribution == "" {
		c.Distribution = "stable"
	}
	return "dists/" + c.Distribution + "/InRelease"
}

// publicKey serves the armored public key of the repo's signing key. It takes the read gate
// of the repo, like its indexes. A proxy has no key of its own: clients use the remote
// archive's key.
func (h *Handler) publicKey(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	if repo.Kind == repos.KindProxy {
		httpx.Error(w, r, http.StatusNotFound, "a proxy repository has no signing key; use the key of the remote archive")
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

// unit is the rebuild unit of a (component, architecture) of a distribution: "main/amd64".
func unit(component, arch string) string { return component + "/" + arch }

// splitUnit splits a rebuild unit into its component and architecture.
func splitUnit(u string) (component, arch string, ok bool) {
	i := strings.LastIndexByte(u, '/')
	if i <= 0 || i == len(u)-1 {
		return "", "", false
	}
	return u[:i], u[i+1:], true
}

// onRepoChange runs in the transaction of a repo update (format.Spec OnRepoChange). A change of
// the architectures marks every (distribution, component) with packages for a rebuild of the
// added and the removed architectures (without an added one, the first configured one): the
// rebuild builds the added ones (arch:all packages appear for them), deletes the index files of
// the removed ones and writes every Release again. A change of the signing key marks one unit of
// every distribution, so each Release is signed again with the new key.
func (h *Handler) onRepoChange(ctx context.Context, tx *sql.Tx, old, new *repos.Repo) error {
	oldArchs, newArchs := architectures(old), architectures(new)
	archsChanged := !slices.Equal(oldArchs, newArchs)
	if !archsChanged && signingKey(old) == signingKey(new) {
		return nil
	}
	var mark []string
	for _, a := range newArchs {
		if !slices.Contains(oldArchs, a) {
			mark = append(mark, a)
		}
	}
	if len(mark) == 0 {
		mark = newArchs[:1]
	}
	for _, a := range oldArchs {
		if archsChanged && !slices.Contains(newArchs, a) {
			mark = append(mark, a)
		}
	}
	units, err := dbq.New(tx).ListPackageComponents(ctx, new.ID)
	if err != nil {
		return err
	}
	for _, u := range units {
		for _, a := range mark {
			if err := h.rebuilds.MarkTx(ctx, tx, new.ID, u.Distribution, unit(u.Component, a)); err != nil {
				return err
			}
		}
	}
	return nil
}

// indexDir is the folder of the Packages files of a unit: dists/{dist}/{comp}/binary-{arch}/.
func indexDir(dist, comp, arch string) string {
	return path.Join("dists", dist, comp, "binary-"+arch) + "/"
}
