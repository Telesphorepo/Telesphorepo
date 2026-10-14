// Package api implements the JSON API under /api/ (spec §10).
package api

import (
	"context"
	"net/http"

	"telesphorepo/internal/assets"
	"telesphorepo/internal/auth"
	"telesphorepo/internal/config"
	"telesphorepo/internal/repos"
)

// DebianStatus is satisfied by *debian.Handler.
type DebianStatus interface {
	Pending(ctx context.Context, repoID int64) (int, error)
}

type API struct {
	auth   *auth.Service
	repos  *repos.Service
	assets *assets.Service
	debian DebianStatus
	cfg    *config.Config
}

func New(a *auth.Service, rs *repos.Service, as *assets.Service, deb DebianStatus, cfg *config.Config) *API {
	return &API{auth: a, repos: rs, assets: as, debian: deb, cfg: cfg}
}

// Handler returns the /api/ handler, already wrapped in http.CrossOriginProtection.
// The server mounts it at "/api/".
func (a *API) Handler() http.Handler { panic("TODO") }
