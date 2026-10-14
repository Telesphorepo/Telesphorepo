// Package maven serves hosted Maven repos at /maven/{repo}/{path...} (spec §8).
package maven

import (
	"net/http"

	"telesphorepo/internal/assets"
	"telesphorepo/internal/auth"
	"telesphorepo/internal/config"
)

type Handler struct {
	auth   *auth.Service
	assets *assets.Service
	cfg    *config.Config
}

func New(a *auth.Service, as *assets.Service, cfg *config.Config) *Handler {
	return &Handler{auth: a, assets: as, cfg: cfg}
}

func (h *Handler) Name() string { return "maven" }

// Register mounts GET/HEAD/PUT /maven/{repo}/{path...}.
func (h *Handler) Register(mux *http.ServeMux) { panic("TODO") }
