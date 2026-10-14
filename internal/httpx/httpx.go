// Package httpx holds small shared HTTP helpers: path validation, JSON I/O, client IP.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ValidRepoPath implements spec §6 for the repo-relative path p (already decoded,
// e.g. r.PathValue("path")). It also inspects r.URL.EscapedPath() for encoded
// separators/NUL. Returns false if the path must be rejected with 400.
func ValidRepoPath(r *http.Request, p string) bool {
	if p == "" || len(p) > 1024 || !utf8.ValidString(p) {
		return false
	}
	esc := strings.ToLower(r.URL.EscapedPath())
	for _, bad := range []string{"%2f", "%5c", "%00"} {
		if strings.Contains(esc, bad) {
			return false
		}
	}
	for _, seg := range strings.Split(esc, "/") {
		if seg == "%2e%2e" || seg == "%2e." || seg == ".%2e" {
			return false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || len(seg) > 255 {
			return false
		}
		for i := 0; i < len(seg); i++ {
			if c := seg[i]; c < 0x20 || c == 0x7F || c == '\\' {
				return false
			}
		}
	}
	return true
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes {"error": msg} with status.
func Error(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// DecodeJSON decodes the body (capped at max bytes, unknown fields rejected) into v.
// On failure it writes 400 (or 413) and returns false.
func DecodeJSON(w http.ResponseWriter, r *http.Request, max int64, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, max))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if _, err = dec.Token(); err == io.EOF {
			return true
		} else if err == nil {
			err = errors.New("trailing data")
		}
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		Error(w, http.StatusRequestEntityTooLarge, "request body too large")
	} else {
		Error(w, http.StatusBadRequest, "invalid JSON body")
	}
	return false
}

type clientIPKey struct{}

// WithClientIP returns r with the resolved client address attached (see internal/netrules,
// which resolves it once per request from the TCP peer and the trusted proxies).
func WithClientIP(r *http.Request, a netip.Addr) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), clientIPKey{}, a))
}

// PeerIP returns the address of the TCP peer of r.
func PeerIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, _ := netip.ParseAddr(host)
	return a.Unmap()
}

// ClientIP returns the client address that WithClientIP attached to r, else the TCP peer.
func ClientIP(r *http.Request) netip.Addr {
	if a, ok := r.Context().Value(clientIPKey{}).(netip.Addr); ok {
		return a
	}
	return PeerIP(r)
}

// Limit parses ?limit= (default 50, clamp 1..200).
func Limit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 50
	}
	return min(max(n, 1), 200)
}
