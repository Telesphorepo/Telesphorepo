package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// loopbackHosts answer as the base URL host: a health check, a port mapping or a reverse proxy
// on the same machine reaches the server by them.
var loopbackHosts = []string{"localhost", "127.0.0.1", "::1"}

// hostSet is what the host check reads from one settings snapshot.
type hostSet struct {
	open  bool            // base_url is empty: every host is served
	own   map[string]bool // the base URL host, the hosts of the modules (module.Extender.Host), the loopback names
	paths []string        // the root prefixes that keep their routes on a repo domain
}

// hosts is the host check in front of the route table (see ServeHTTP).
type hosts struct {
	set     *settings.Service
	repos   *repos.Service
	formats *format.Registry
	mux     *route.Mux
	view    *settings.View[*hostSet]
	next    http.Handler
}

func newHosts(set *settings.Service, rs *repos.Service, formats *format.Registry, mux *route.Mux,
	extra []func(*settings.Snapshot) string, paths []string) *hosts {
	h := &hosts{set: set, repos: rs, formats: formats, mux: mux, next: mux.Handler()}
	h.view = settings.NewView(func(s *settings.Snapshot) *hostSet {
		hs := &hostSet{own: map[string]bool{}, paths: paths}
		base := settings.BaseURL.Get(s)
		if base == "" {
			hs.open = true
		} else if u, err := url.Parse(base); err == nil && u.Hostname() != "" {
			hs.own[strings.ToLower(u.Hostname())] = true
		}
		for _, fn := range extra {
			if host := fn(s); host != "" {
				hs.own[host] = true
			}
		}
		for _, l := range loopbackHosts {
			hs.own[l] = true
		}
		return hs
	})
	return h
}

// hostOf is the host of a Host header: lower case, without the port, the brackets of an IPv6
// address and a trailing dot.
func hostOf(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	} else {
		h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// ServeHTTP checks the Host of the request (never X-Forwarded-Host):
//
//  1. a repo domain (repo_domains): the request reads the domain's repo (domain);
//  2. base_url is empty: every host is served (setup and the first sign-in need it);
//  3. the base URL host, a host of a module (the Docker upload host) or a loopback name: served;
//  4. GET /healthz: answered on any host, for container and load balancer health checks;
//  5. any other: 421 Misdirected Request.
//
// It costs a map lookup per request: the sets are views of the settings snapshot.
func (h *hosts) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	snap := h.set.Current()
	host := hostOf(r.Host)
	if name, ok := settings.DomainsOf(snap)[host]; ok {
		h.domain(w, r, name, h.view.Get(snap).paths)
		return
	}
	hs := h.view.Get(snap)
	if hs.open || hs.own[host] || (r.URL.Path == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead)) {
		h.next.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusMisdirectedRequest)
	io.WriteString(w, "This server does not answer for this host.\n")
}

// domain serves a request to the repo domain of repo name. It only reads: another method is 405.
// "/" is 404; a path under a domain path (module.Extender.DomainPath) keeps its route; any other
// path P is the request /<format>/<repo>P on the same route and policy, its escaping kept, so
// httpx.RepoPath checks it as on the base URL. The policy then reads no UI session
// (route.OnRepoDomain), and a Location of the answer under /<format>/<repo>/ becomes a path of
// the domain.
func (h *hosts) domain(w http.ResponseWriter, r *http.Request, name string, paths []string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		httpx.Error(w, r, http.StatusMethodNotAllowed, "this domain only reads; publish at the base URL of the server")
		return
	}
	p := r.URL.Path
	for _, dp := range paths {
		if strings.HasPrefix(p, dp) {
			h.next.ServeHTTP(w, route.WithRepoDomain(r))
			return
		}
	}
	if p == "/" || p == "" {
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	// As the route table does: an unclean path is a redirect to its clean form, on the domain.
	if c := cleanPath(p); c != p {
		u := &url.URL{Path: c, RawQuery: r.URL.RawQuery}
		w.Header().Set("Location", u.String())
		w.WriteHeader(http.StatusMovedPermanently)
		return
	}
	repo, err := h.repos.ByName(r.Context(), name)
	switch {
	case errors.Is(err, repos.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return
	case err != nil:
		route.Fail(w, r, err)
		return
	case !h.mux.RepoRooted(repo.Format):
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	prefix := "/" + repo.Format + "/" + repo.Name // repo names need no escaping (repos.NameRE)
	rr := route.WithRepoDomain(r)
	u := *r.URL
	u.Path = prefix + r.URL.Path
	if r.URL.RawPath != "" {
		u.RawPath = prefix + r.URL.RawPath
	}
	rr.URL = &u
	h.next.ServeHTTP(&domainWriter{ResponseWriter: w, prefix: prefix + "/"}, rr)
}

// cleanPath is the clean form of p that the route table redirects to: no empty, "." or ".."
// segment, the trailing slash kept.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c
}

// domainWriter turns a Location under prefix ("/<format>/<repo>/", e.g. a redirect of a PyPI
// index to its slash form) into the same path on the repo domain before the header is sent.
type domainWriter struct {
	http.ResponseWriter
	prefix string
	sent   bool
}

func (w *domainWriter) fix() {
	if w.sent {
		return
	}
	w.sent = true
	h := w.Header()
	if l := h.Get("Location"); strings.HasPrefix(l, w.prefix) {
		h.Set("Location", l[len(w.prefix)-1:])
	}
}

func (w *domainWriter) WriteHeader(code int) { w.fix(); w.ResponseWriter.WriteHeader(code) }

func (w *domainWriter) Write(b []byte) (int, error) { w.fix(); return w.ResponseWriter.Write(b) }

func (w *domainWriter) ReadFrom(r io.Reader) (int64, error) {
	w.fix()
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(w.ResponseWriter, r)
}

func (w *domainWriter) Flush() { w.fix(); http.NewResponseController(w.ResponseWriter).Flush() }

func (w *domainWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// checkDomains is the check of a change of the general settings (settings.Service.AddCheck): a
// repo domain is no host the server answers for itself, and each domain that the change adds or
// points elsewhere names a repo (or a redirect of one) of a format whose read routes start with
// /<format>/{repo}/.
func (h *hosts) checkDomains(d *settings.Draft) {
	prev := map[string]string{}
	if d.Prev != nil {
		prev = settings.DomainsOf(d.Prev)
	}
	next := settings.DomainsOf(d.Next)
	own := h.view.Get(d.Next).own
	var msgs []string
	for _, host := range sortedKeys(next) {
		name := next[host]
		if own[host] {
			msgs = append(msgs, host+" is an address of this server itself")
			continue
		}
		if prev[host] == name {
			continue
		}
		repo, err := h.repos.ByName(context.Background(), name)
		switch {
		case errors.Is(err, repos.ErrNotFound):
			msgs = append(msgs, fmt.Sprintf("%s: there is no repository %s", host, name))
		case err != nil:
			msgs = append(msgs, fmt.Sprintf("%s: the repository %s could not be read: %v", host, name, err))
		case !h.mux.RepoRooted(repo.Format):
			title := repo.Format
			if s := h.formats.Get(repo.Format); s != nil {
				title = s.Title
			}
			msgs = append(msgs, fmt.Sprintf("%s: %s is a %s repository, whose clients do not read at /%s/%s/; a domain cannot serve it", host, repo.Name, title, repo.Format, repo.Name))
		}
	}
	if len(msgs) > 0 {
		d.Fail(settings.RepoDomains.Name(), strings.Join(msgs, "; "))
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
