// Package debian serves hosted Debian repos (spec §9): uploads, signed incremental indexes.
package debian

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"telesphorepo/internal/assets"
	"telesphorepo/internal/auth"
	"telesphorepo/internal/config"
	"telesphorepo/internal/db"
	"telesphorepo/internal/httpx"
	"telesphorepo/internal/repos"
	"telesphorepo/internal/signing"
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
