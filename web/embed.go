// Package web embeds the built React UI (web/dist) and serves it with SPA fallback.
package web

import "net/http"

// Handler serves static files from the embedded dist/ and falls back to index.html for
// unknown paths that don't look like files.
func Handler() http.Handler { panic("TODO") }
