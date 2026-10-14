// Package debian is the Debian (apt) format plugin (spec §9): uploads, signed incremental
// indexes.
package debian

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/debian/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// name is the format name: URL segment, repos.format value, settings group and migration track.
const name = "debian"

// Settings of the debian group.
const (
	KeyDebounce = "deb.debounce"
	KeyMaxDelay = "deb.max_delay"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed queries/*.sql
var queriesFS embed.FS

func sub(f embed.FS, dir string) fs.FS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic(err) // the embedded directory always exists
	}
	return s
}

type Handler struct {
	db       *db.DB
	q        *dbq.Queries // on the reader pool
	auth     *auth.Service
	assets   *assets.Service
	repos    *repos.Service
	cfg      config.Provider
	settings *settings.Service
	log      *slog.Logger

	signer func() *signing.Signer // current key (live; nil = unsigned indexes)

	clockMu sync.Mutex
	clock   int64 // last issued deb_dirty timestamp (monotonic, unix nanos)

	idx indexer

	host format.Host // the core services, for the audit log
}

// New returns the Debian format. Register supplies the core services.
func New() format.Format {
	h := &Handler{}
	h.idx.init(h)
	return h
}

func (h *Handler) Name() string  { return name }
func (h *Handler) Title() string { return "Debian (apt)" }

// Register mounts /debian/{repo}/upload/{dist}/{component} (PUT, POST),
// GET/HEAD /debian/{repo}/{path...} (dists/ and pool/) and GET /keys/debian.asc. Configuration
// and the signing key are read on every use, so setting changes apply live.
func (h *Handler) Register(mux *http.ServeMux, host format.Host) {
	h.host = host
	h.db, h.q = host.DB, dbq.New(host.DB.R)
	h.auth, h.assets, h.repos = host.Auth, host.Assets, host.Repos
	h.cfg, h.settings, h.signer, h.log = host.Config, host.Settings, host.Signer, host.Log

	mux.HandleFunc("PUT /debian/{repo}/upload/{dist}/{component}", h.upload)
	mux.HandleFunc("POST /debian/{repo}/upload/{dist}/{component}", h.upload)
	mux.HandleFunc("GET /debian/{repo}/{path...}", h.download)
	mux.HandleFunc("GET /keys/debian.asc", h.publicKey)
}

// Migrations holds the deb_* tables.
func (h *Handler) Migrations() fs.FS { return sub(migrationsFS, "migrations") }

// QuerySet lists the plugin's queries for the plan guard.
func (h *Handler) QuerySet() db.QuerySet {
	return db.QuerySet{
		Component: name,
		Files:     sub(queriesFS, "queries"),
		Extra:     dbq.Extra,
		Allow: map[string]string{
			"ListAllDirtyDists": "startup recovery: enqueues every pending rebuild once per start; deb_dirty holds only units not yet rebuilt",
			"ListAllIndexFiles": "background re-sign after a signing-key change, an admin action; two rows per (dist, component, architecture)",
		},
	}
}

// Settings returns the debian settings group.
func (h *Handler) Settings() []settings.Def {
	return []settings.Def{
		{Key: KeyDebounce, Type: settings.TypeDuration, Default: "2s", Dev: "300ms", Label: "Index rebuild delay",
			Help: "Quiet period after an upload before the Debian indexes are rebuilt."},
		{Key: KeyMaxDelay, Type: settings.TypeDuration, Default: "30s", Label: "Max index rebuild delay",
			Help: "Longest a rebuild waits under continuous uploads."},
	}
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	if !httpx.ValidRepoPath(r, p) {
		httpx.Error(w, http.StatusBadRequest, "invalid path")
		return
	}
	repo, _, ok := h.auth.RepoGate(w, r, r.PathValue("repo"), name, auth.Read, false)
	if !ok {
		return
	}
	if !strings.HasPrefix(p, "dists/") && !strings.HasPrefix(p, "pool/") {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	h.assets.Serve(w, r, repo, p)
}

func (h *Handler) publicKey(w http.ResponseWriter, r *http.Request) {
	signer := h.signer()
	if signer == nil {
		httpx.Error(w, http.StatusNotFound, "no signing key configured")
		return
	}
	key := signer.PublicKeyArmored()
	w.Header().Set("Content-Type", "application/pgp-keys")
	w.Header().Set("Content-Length", fmt.Sprint(len(key)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(key)
	}
}

// Start launches the background indexer, enqueues all durable deb_dirty rows and re-signs the
// indexes whenever the signing key changes. It returns immediately; the indexer stops when ctx
// is cancelled.
func (h *Handler) Start(ctx context.Context) {
	if h.signer() == nil {
		h.log.Warn("no usable signing key: Debian indexes are unsigned; clients need [trusted=yes]")
	}
	h.settings.Subscribe(h.signingKeyChanged)
	h.idx.start(ctx)
}

// Status reports the number of units waiting for an index rebuild, counting rows still dirty
// and rows being rebuilt.
func (h *Handler) Status(ctx context.Context, repo *repos.Repo) (any, error) {
	n, err := h.q.CountDirty(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	return struct {
		Pending int64 `json:"pending"`
	}{n}, nil
}

// RepoConfigChanged applies an architecture change. It removes the index files of removed
// architectures (so they are no longer served or listed in Release) and their pending
// rebuilds, then marks the added architectures of every (dist, component) with packages for a
// rebuild, so arch:all packages appear for them. Every dist is rebuilt, so each Release lists
// the new architectures; without an added architecture the first configured one is marked.
func (h *Handler) RepoConfigChanged(ctx context.Context, old, new *repos.Repo) error {
	oldArchs, newArchs := architectures(old), architectures(new)
	if slices.Equal(oldArchs, newArchs) {
		return nil
	}
	var removed, added []string
	for _, a := range oldArchs {
		if !slices.Contains(newArchs, a) {
			removed = append(removed, a)
		}
	}
	for _, a := range newArchs {
		if !slices.Contains(oldArchs, a) {
			added = append(added, a)
		}
	}
	mark := added
	if len(mark) == 0 {
		mark = newArchs[:1]
	}

	var dists, dropped []string
	err := h.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		if len(removed) > 0 {
			files, err := q.ListRepoIndexFiles(ctx, new.ID)
			if err != nil {
				return err
			}
			for _, f := range files {
				if _, arch, ok := unitOf(f.Path); !ok || !slices.Contains(removed, arch) {
					continue
				}
				if err := q.DeleteIndexFile(ctx, dbq.DeleteIndexFileParams{RepoID: new.ID, Distribution: f.Distribution, Path: f.Path}); err != nil {
					return err
				}
				p := "dists/" + f.Distribution + "/" + f.Path
				if err := q.DeleteIndexAsset(ctx, dbq.DeleteIndexAssetParams{RepoID: new.ID, Path: p}); err != nil {
					return err
				}
				dropped = append(dropped, p)
			}
			for _, a := range removed {
				if err := q.DeleteDirtyArch(ctx, dbq.DeleteDirtyArchParams{RepoID: new.ID, Architecture: a}); err != nil {
					return err
				}
			}
		}
		units, err := q.ListPackageComponents(ctx, new.ID)
		if err != nil {
			return err
		}
		ts := h.tick()
		for _, u := range units {
			for _, a := range mark {
				if err := q.MarkDirty(ctx, dbq.MarkDirtyParams{RepoID: new.ID, Distribution: u.Distribution,
					Component: u.Component, Architecture: a, MarkedAt: ts}); err != nil {
					return err
				}
			}
		}
		dists, err = q.ListRepoDirtyDists(ctx, new.ID)
		return err
	})
	if err != nil {
		return err
	}
	var dirs []string
	for _, p := range dropped {
		unlock := h.assets.Lock(new.ID, p)
		if err := h.assets.Store().Delete(ctx, assets.Key(new.Name, p)); err != nil {
			h.log.Warn("delete index file of a removed architecture", "repo", new.Name, "path", p, "err", err)
		}
		unlock()
		if d := path.Dir(p); !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	// No index of a removed architecture is served any more, so none of its by-hash files is.
	for _, d := range dirs {
		if err := h.pruneByHash(ctx, new, d, nil); err != nil {
			h.log.Warn("delete by-hash files of a removed architecture", "repo", new.Name, "dir", d, "err", err)
		}
	}
	for _, d := range dists {
		h.idx.notify(distKey{new.ID, d})
	}
	return nil
}

// unitOf splits an index file path relative to dists/{dist}/, e.g.
// main/binary-amd64/Packages.gz, into its component and architecture.
func unitOf(p string) (component, arch string, ok bool) {
	component, rest, ok := strings.Cut(p, "/binary-")
	if !ok {
		return "", "", false
	}
	arch, _, ok = strings.Cut(rest, "/")
	return component, arch, ok && component != "" && arch != ""
}

// tick returns a strictly increasing timestamp for deb_dirty.marked_at and rebuild starts.
// Callers invoke it while holding the single SQLite writer, so a rebuild start is greater
// than the mark of every committed upload and smaller than every later one.
func (h *Handler) tick() int64 {
	h.clockMu.Lock()
	defer h.clockMu.Unlock()
	n := nowNanos()
	if n <= h.clock {
		n = h.clock + 1
	}
	h.clock = n
	return n
}

func (h *Handler) raiseClock(n int64) {
	h.clockMu.Lock()
	if n > h.clock {
		h.clock = n
	}
	h.clockMu.Unlock()
}

// signingKeyChanged is a settings subscriber: when the signing key differs between old and new
// it marks one unit of every dist with index files dirty, so each Release is signed again.
func (h *Handler) signingKeyChanged(old, new *settings.Snapshot) {
	if fingerprint(old) == fingerprint(new) {
		return
	}
	// Subscribers run inside the settings writer; do the DB work elsewhere.
	go func() {
		if err := h.resignAll(context.Background()); err != nil {
			h.log.Error("mark indexes dirty after signing key change", "err", err)
		}
	}()
}

func fingerprint(s *settings.Snapshot) string {
	if s == nil || s.Signer == nil {
		return ""
	}
	return s.Signer.Fingerprint()
}

func (h *Handler) resignAll(ctx context.Context) error {
	var keys []distKey
	err := h.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		files, err := q.ListAllIndexFiles(ctx)
		if err != nil {
			return err
		}
		ts := h.tick()
		for _, f := range files {
			k := distKey{f.RepoID, f.Distribution}
			if len(keys) > 0 && keys[len(keys)-1] == k {
				continue // files are ordered by (repo, dist): one unit per dist is enough
			}
			comp, arch, ok := unitOf(f.Path)
			if !ok {
				continue
			}
			if err := q.MarkDirty(ctx, dbq.MarkDirtyParams{RepoID: f.RepoID, Distribution: f.Distribution,
				Component: comp, Architecture: arch, MarkedAt: ts}); err != nil {
				return err
			}
			keys = append(keys, k)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range keys {
		h.idx.notify(k)
	}
	return nil
}
