// Package httpx holds small shared HTTP helpers: path validation, JSON I/O, client IP.
package httpx

import (
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

func inPrefixes(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP returns RemoteAddr's IP, or the right-most X-Forwarded-For hop that is not
// itself trusted when RemoteAddr is inside a trusted prefix (spec §7.4).
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote, _ := netip.ParseAddr(host)
	remote = remote.Unmap()
	if !inPrefixes(remote, trusted) {
		return remote
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return remote
		}
		a = a.Unmap()
		if !inPrefixes(a, trusted) {
			return a
		}
	}
	return remote
}

// Limit parses ?limit= (default 50, clamp 1..200).
func Limit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 50
	}
	return min(max(n, 1), 200)
}
