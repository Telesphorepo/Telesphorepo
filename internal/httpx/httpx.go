// Package httpx holds small shared HTTP helpers: path validation, JSON I/O, client IP.
package httpx

import (
	"net/http"
	"net/netip"
)

// ValidRepoPath implements spec §6 for the repo-relative path p (already decoded,
// e.g. r.PathValue("path")). It also inspects r.URL.EscapedPath() for encoded
// separators/NUL. Returns false if the path must be rejected with 400.
func ValidRepoPath(r *http.Request, p string) bool { panic("TODO") }

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) { panic("TODO") }

// Error writes {"error": msg} with status.
func Error(w http.ResponseWriter, status int, msg string) { panic("TODO") }

// DecodeJSON decodes the body (capped at max bytes, unknown fields rejected) into v.
// On failure it writes 400 (or 413) and returns false.
func DecodeJSON(w http.ResponseWriter, r *http.Request, max int64, v any) bool { panic("TODO") }

// ClientIP returns RemoteAddr's IP, or the right-most X-Forwarded-For hop that is not
// itself trusted when RemoteAddr is inside a trusted prefix (spec §7.4).
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr { panic("TODO") }

// Limit parses ?limit= (default 50, clamp 1..200).
func Limit(r *http.Request) int { panic("TODO") }
