// Package debian serves hosted Debian repos (spec §9): uploads, signed incremental indexes.
package debian

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

type Handler struct {
	db     *db.DB
	auth   *auth.Service
	assets *assets.Service
	repos  *repos.Service
	cfg    *config.Config
	log    *slog.Logger

	signer *signing.Signer // nil = unsigned indexes

	clockMu sync.Mutex
	clock   int64 // last issued deb_dirty timestamp (monotonic, unix nanos)

	idx indexer
}

// New loads the signing key (cfg.GPGKeyFile, optional passphrase file) if configured.
func New(d *db.DB, a *auth.Service, as *assets.Service, rs *repos.Service, cfg *config.Config, log *slog.Logger) (*Handler, error) {
	h := &Handler{db: d, auth: a, assets: as, repos: rs, cfg: cfg, log: log}
	if cfg.GPGKeyFile != "" {
		s, err := signing.Load(cfg.GPGKeyFile, cfg.GPGPassphraseFile)
		if err != nil {
			return nil, fmt.Errorf("debian: load signing key: %w", err)
		}
		h.signer = s
	}
	h.idx.init(h)
	return h, nil
}

func (h *Handler) Name() string { return "debian" }

// Register mounts /debian/{repo}/upload/{dist}/{component} (PUT, POST),
// GET/HEAD /debian/{repo}/{path...} (dists/ and pool/) and GET /keys/debian.asc.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("PUT /debian/{repo}/upload/{dist}/{component}", h.upload)
	mux.HandleFunc("POST /debian/{repo}/upload/{dist}/{component}", h.upload)
	mux.HandleFunc("GET /debian/{repo}/{path...}", h.download)
	mux.HandleFunc("GET /keys/debian.asc", h.publicKey)
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	if !httpx.ValidRepoPath(r, p) {
		httpx.Error(w, http.StatusBadRequest, "invalid path")
		return
	}
	repo, _, ok := h.auth.RepoGate(w, r, r.PathValue("repo"), repos.FormatDebian, auth.Read, false)
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
	if h.signer == nil {
		httpx.Error(w, http.StatusNotFound, "no signing key configured")
		return
	}
	key := h.signer.PublicKeyArmored()
	w.Header().Set("Content-Type", "application/pgp-keys")
	w.Header().Set("Content-Length", fmt.Sprint(len(key)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(key)
	}
}

// Start launches the background indexer and enqueues all durable deb_dirty rows.
// It returns immediately; the indexer stops when ctx is cancelled.
func (h *Handler) Start(ctx context.Context) {
	if h.signer == nil {
		h.log.Warn("no TR_GPG_KEY_FILE configured: Debian indexes are unsigned; clients need [trusted=yes]")
	}
	h.idx.start(ctx)
}

// Pending returns the number of deb_dirty rows for repoID (for /api/repos/{name}/debian/status).
// It must count rows still dirty OR currently being rebuilt.
func (h *Handler) Pending(ctx context.Context, repoID int64) (int, error) {
	var n int
	err := h.db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM deb_dirty WHERE repo_id = ?`, repoID).Scan(&n)
	return n, err
}

// ConfigChanged marks for rebuild every (dist, component, arch) of repo whose configured
// architecture has no Packages index yet although the component holds packages, so
// arch:all packages appear for architectures added after they were uploaded.
func (h *Handler) ConfigChanged(ctx context.Context, repo *repos.Repo) error {
	if repo.Format != repos.FormatDebian {
		return nil
	}
	var dists []string
	err := h.db.Tx(ctx, func(tx *sql.Tx) error {
		mark := h.tick()
		for _, arch := range repo.Debian().Architectures {
			if _, err := tx.ExecContext(ctx, `INSERT INTO deb_dirty (repo_id, distribution, component, architecture, marked_at)
				SELECT DISTINCT p.repo_id, p.distribution, p.component, ?, ? FROM deb_packages p
				WHERE p.repo_id = ? AND NOT EXISTS (SELECT 1 FROM deb_index_files f
					WHERE f.repo_id = p.repo_id AND f.distribution = p.distribution
					AND f.path = p.component || '/binary-' || ? || '/Packages')
				ON CONFLICT (repo_id, distribution, component, architecture) DO UPDATE SET marked_at = excluded.marked_at`,
				arch, mark, repo.ID, arch); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT distribution FROM deb_dirty WHERE repo_id = ?`, repo.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				return err
			}
			dists = append(dists, d)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, d := range dists {
		h.idx.notify(distKey{repo.ID, d})
	}
	return nil
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
