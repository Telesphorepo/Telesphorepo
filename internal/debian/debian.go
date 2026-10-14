// Package debian serves hosted Debian repos (spec §9): uploads, signed incremental indexes.
package debian

import (
	"context"
	"log/slog"
	"net/http"

	"telesphorepo/internal/assets"
	"telesphorepo/internal/auth"
	"telesphorepo/internal/config"
	"telesphorepo/internal/db"
	"telesphorepo/internal/repos"
)

type Handler struct {
	db     *db.DB
	auth   *auth.Service
	assets *assets.Service
	repos  *repos.Service
	cfg    *config.Config
	log    *slog.Logger
	// signer, indexer state go here
}

// New loads the signing key (cfg.GPGKeyFile, optional passphrase file) if configured.
func New(d *db.DB, a *auth.Service, as *assets.Service, rs *repos.Service, cfg *config.Config, log *slog.Logger) (*Handler, error) {
	panic("TODO")
}

func (h *Handler) Name() string { return "debian" }

// Register mounts /debian/{repo}/upload/{dist}/{component} (PUT, POST),
// GET/HEAD /debian/{repo}/{path...} (dists/ and pool/) and GET /keys/debian.asc.
func (h *Handler) Register(mux *http.ServeMux) { panic("TODO") }

// Start launches the background indexer and enqueues all durable deb_dirty rows.
// It returns immediately; the indexer stops when ctx is cancelled.
func (h *Handler) Start(ctx context.Context) { panic("TODO") }

// Pending returns the number of deb_dirty rows for repoID (for /api/repos/{name}/debian/status).
// It must count rows still dirty OR currently being rebuilt.
func (h *Handler) Pending(ctx context.Context, repoID int64) (int, error) { panic("TODO") }
