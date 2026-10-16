package docker

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// The registry API version header of every /v2/ answer.
const (
	apiVersionHeader = "Docker-Distribution-API-Version"
	apiVersion       = "registry/2.0"
)

// registry adds what every /v2/ route of a repo has: bearer tokens, the OCI errors, the Bearer
// challenge, the upload token of an upload URL and the repo from the first name segment.
func (f *Format) registry(p route.Policy) route.Policy {
	p.Bearer, p.Errors, p.Challenge, p.URLToken = true, refuse, f.challenge, uploadToken
	p.Repo.Name = repoOf
	return p
}

// routes mounts the registry API at /v2/ and the token endpoint at /docker/.
func (f *Format) routes(r *route.Router, k format.Kit) {
	r.Handle("GET /v2/{$}", route.Public, f.ping)
	r.Handle("GET /v2/_catalog", route.Policy{Accept: auth.CredPassword | auth.CredReadToken | auth.CredPublish | auth.CredDevice, Anonymous: true,
		Bearer: true, Errors: refuse, Challenge: f.challenge}, f.catalog)
	r.Handle("GET /v2/{rest...}", f.registry(route.FormatRead(name)), f.get)
	r.Handle("PUT /v2/{rest...}", f.registry(route.FormatWrite(name)), f.put)
	r.Handle("POST /v2/{rest...}", f.registry(route.FormatWrite(name)), f.post)
	r.Handle("PATCH /v2/{rest...}", f.registry(route.FormatWrite(name)), f.patch)
	r.Handle("DELETE /v2/{rest...}", f.registry(route.Policy{Accept: auth.CredPublish, Repo: &route.RepoRule{Format: name,
		Ops:  []route.RepoOp{{Op: access.Write, Perm: perm.RepoPublish}, {Op: access.Delete, Perm: perm.RepoDelete}},
		Pick: pickDelete}}), f.delete)
	r.Handle("GET /docker/token", route.Public, f.token)
	r.Handle("POST /docker/token", route.Public, f.token)
	r.Handle("GET /docker/hosts", route.Public, f.hosts)
}

// pickDelete is the operation of a DELETE: the cancel of an upload writes (repo.publish), every
// other delete deletes (repo.delete).
func pickDelete(r *http.Request) int {
	if t, ok, err := parseTarget(r); ok && err == nil && t.action == actUploads {
		return 0
	}
	return 1
}

// hostOf returns the scheme and the host that the request reached the registry at: those of
// docker.upload_url when the request came to the upload host (uploadHostOf), else mainHostOf.
func (f *Format) hostOf(r *http.Request) (scheme, host string) {
	if u, ok := f.uploadHostOf(r); ok {
		return u.Scheme, u.Host
	}
	return f.mainHostOf(r)
}

// mainHostOf returns the scheme and the host of the main address of the registry: those of
// base_url, else the scheme that the client used (httpx.ClientScheme, which follows a trusted
// proxy) and the host of the request.
func (f *Format) mainHostOf(r *http.Request) (scheme, host string) {
	if u, ok := urlOf(settings.BaseURL.Get(f.h.Settings.Current())); ok {
		return u.Scheme, u.Host
	}
	return httpx.ClientScheme(r), r.Host
}

// uploadHostOf returns docker.upload_url when direct uploads are on and the Host of r is its host.
// A default port (:443 for https, :80 for http) counts as none.
func (f *Format) uploadHostOf(r *http.Request) (*url.URL, bool) {
	u, ok := f.directUploads()
	if !ok {
		return nil, false
	}
	return u, strings.EqualFold(withoutDefaultPort(u.Scheme, u.Host), withoutDefaultPort(u.Scheme, r.Host))
}

// directUploads returns docker.upload_url when docker.direct_uploads is on.
func (f *Format) directUploads() (*url.URL, bool) {
	set := f.h.Settings.Current()
	if !DirectUploads.Get(set) {
		return nil, false
	}
	return urlOf(UploadURL.Get(set))
}

// withoutDefaultPort returns host without the default port of scheme.
func withoutDefaultPort(scheme, host string) string {
	if h, port, err := net.SplitHostPort(host); err == nil && (scheme == "https" && port == "443" || scheme == "http" && port == "80") {
		if strings.Contains(h, ":") {
			return "[" + h + "]"
		}
		return h
	}
	return host
}

// urlOf parses an http(s) URL setting; ok is false when it is empty or has no host.
func urlOf(s string) (*url.URL, bool) {
	if s == "" {
		return nil, false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, false
	}
	return u, true
}

// registryHost returns the host of baseURL ("" without one).
func registryHost(baseURL string) string {
	if u, ok := urlOf(baseURL); ok {
		return u.Host
	}
	return ""
}

// challengeFor is the WWW-Authenticate header of a /v2/ request: the realm of the token endpoint,
// the service, and the scope of the request's name with the actions of its method.
func (f *Format) challengeFor(r *http.Request, d access.Decision) string {
	scheme, host := f.hostOf(r)
	v := `Bearer realm="` + scheme + "://" + host + `/docker/token",service="` + host + `"`
	if t, ok, err := parseTarget(r); ok && err == nil {
		actions := "pull"
		switch r.Method {
		case http.MethodPut, http.MethodPost, http.MethodPatch:
			actions = "pull,push"
		case http.MethodDelete:
			actions = "delete"
		}
		v += `,scope="repository:` + t.name + ":" + actions + `"`
	} else if r.URL.Path == "/v2/_catalog" {
		v += `,scope="registry:catalog:*"`
	}
	if d.Code == access.CodeScope {
		v += `,error="insufficient_scope"`
	}
	return v
}

// challenge is the route.Challenger of the /v2/ routes.
func (f *Format) challenge(w http.ResponseWriter, r *http.Request, d access.Decision) {
	w.Header().Set("WWW-Authenticate", f.challengeFor(r, d))
}

// ping answers GET /v2/: 401 with the challenge without a credential, 200 {} with a credential
// that verifies, 401 with one that does not.
func (f *Format) ping(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	w.Header().Set(apiVersionHeader, apiVersion)
	if r.Header.Get("Authorization") == "" {
		w.Header().Set("WWW-Authenticate", f.challengeFor(r, access.Decision{}))
		writeOCI(w, ociErr(http.StatusUnauthorized, codeUnauthorized, "authentication required"))
		return
	}
	_, err := f.h.Auth.Identify(r, auth.Read{Bearer: true})
	if d, ok := auth.RetryAfter(err); ok {
		w.Header().Set("Retry-After", retryAfter(d))
		writeOCI(w, ociErr(http.StatusTooManyRequests, codeTooManyRequests, auth.ErrRateLimited.Error()))
		return
	}
	if errors.Is(err, auth.ErrBadCredentials) {
		w.Header().Set("WWW-Authenticate", f.challengeFor(r, access.Decision{}))
		writeOCI(w, ociErr(http.StatusUnauthorized, codeUnauthorized, auth.ErrBadCredentials.Error()))
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct{}{})
}

// hosts answers GET /docker/hosts: the host of the registry, and the upload host of
// docker.upload_url when it is set (ORAS and Helm 4 push there).
func (f *Format) hosts(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	_, registry := f.mainHostOf(r)
	out := map[string]string{"registry": registry}
	if u, ok := f.directUploads(); ok {
		out["upload"] = u.Host
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// location is the path of an object of name for the Location header.
func location(name, action, ref string) string {
	return "/v2/" + name + "/" + action + "/" + strings.TrimPrefix(ref, "/")
}
