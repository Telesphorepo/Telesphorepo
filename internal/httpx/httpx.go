// Package httpx holds small shared HTTP helpers: the repo path of a URL, JSON I/O, the error
// answers (problem documents), the client.
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

	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
)

// RepoPath returns the repo path in the path value name of r (already decoded, e.g. the
// {path...} of a format route), checked with repopath.Parse. It refuses a raw URL path with an
// encoded separator, backslash or NUL, or an encoded "." or ".." segment, which decode into a
// path that the router did not match.
func RepoPath(r *http.Request, name string) (repopath.Path, error) {
	esc := strings.ToLower(r.URL.EscapedPath())
	for _, bad := range []string{"%2f", "%5c", "%00"} {
		if strings.Contains(esc, bad) {
			return "", repopath.ErrInvalid
		}
	}
	for _, seg := range strings.Split(esc, "/") {
		if seg == "%2e%2e" || seg == "%2e." || seg == ".%2e" {
			return "", repopath.ErrInvalid
		}
	}
	return repopath.Parse(r.PathValue(name))
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// StatusError is an error that carries the HTTP status of its answer, e.g. a refused write.
type StatusError interface {
	error
	HTTPStatus() int
}

// Error writes the error msg with status in the form of the route of r (see WriteProblem): a
// problem document of type "about:blank" with the status text as its title and msg as its
// detail. A 429 is the problem rate-limited.
func Error(w http.ResponseWriter, r *http.Request, status int, msg string) {
	WriteProblem(w, r, Blank(status, msg))
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
		Error(w, r, http.StatusRequestEntityTooLarge, "request body too large")
	} else {
		Error(w, r, http.StatusBadRequest, "invalid JSON body")
	}
	return false
}

type clientKey struct{}

// Client is what the server knows of the client of a request: its address, whether it
// reached the server over HTTPS (directly, or through a TLS-terminating proxy), and the host
// it asked for.
type Client struct {
	IP    netip.Addr
	HTTPS bool
	Host  string // r.Host, set by WithClient
}

// WithClient returns r with the resolved client attached (see internal/netrules, which resolves
// it once per request from the TCP peer and the trusted proxies).
func WithClient(r *http.Request, c Client) *http.Request {
	c.Host = r.Host
	return r.WithContext(context.WithValue(r.Context(), clientKey{}, c))
}

// Origin returns the scheme and host by which the client of the request of ctx reached the
// server, e.g. "https://repo.example.com", or "" for a context without a resolved client.
func Origin(ctx context.Context) string {
	c, ok := ctx.Value(clientKey{}).(Client)
	if !ok || c.Host == "" {
		return ""
	}
	if c.HTTPS {
		return "https://" + c.Host
	}
	return "http://" + c.Host
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

// ClientIP returns the client address that WithClient attached to r, else the TCP peer.
func ClientIP(r *http.Request) netip.Addr {
	if c, ok := r.Context().Value(clientKey{}).(Client); ok {
		return c.IP
	}
	return PeerIP(r)
}

// ClientScheme returns the scheme that the client used to reach the server: "https" or "http".
// It is the one that WithClient attached to r, else that of the connection.
func ClientScheme(r *http.Request) string {
	c, ok := r.Context().Value(clientKey{}).(Client)
	if ok && c.HTTPS || !ok && r.TLS != nil {
		return "https"
	}
	return "http"
}

// Limit parses ?limit= (default 50, clamp 1..200).
func Limit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 50
	}
	return min(max(n, 1), 200)
}
