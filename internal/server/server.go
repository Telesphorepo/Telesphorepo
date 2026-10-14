// Package server wires config, DB, storage, auth, formats, API and UI together.
package server

import (
	"context"

	"telesphorepo/internal/config"
)

// Run starts the HTTP server and blocks until ctx is cancelled (graceful shutdown).
// Steps: slog JSON logger at cfg.LogLevel; mkdir DataDir/TmpDir; db.Open; storage (fs|s3);
// repos/auth/assets services; bootstrap admin if no users; debian.New + Start; maven.New;
// mux: GET /healthz, /api/ -> api.Handler(), formats Register, "/" -> web UI (SPA fallback).
// Access logging must never log Authorization headers, cookies, or query strings.
func Run(ctx context.Context, cfg *config.Config) error { panic("TODO") }
