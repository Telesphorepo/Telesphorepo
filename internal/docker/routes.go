package docker

import (
	"errors"
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
// challenge and the repo from the first name segment.
func (f *Format) registry(p route.Policy) route.Policy {
	p.Bearer, p.Errors, p.Challenge = true, refuse, f.challenge
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

// hostOf returns the scheme and the host that clients reach the registry at: those of
// docker.push_url when the request came to its host, else those of base_url, else the scheme that
// the client used (httpx.ClientScheme, which follows a trusted proxy) and the host of the request.
func (f *Format) hostOf(r *http.Request) (scheme, host string) {
	set := f.h.Settings.Current()
	if u, ok := urlOf(PushURL.Get(set)); ok && strings.EqualFold(u.Host, r.Host) {
		return u.Scheme, u.Host
	}
	if u, ok := urlOf(settings.BaseURL.Get(set)); ok {
		return u.Scheme, u.Host
	}
	return httpx.ClientScheme(r), r.Host
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

// hostsOf returns the registry host (of baseURL) and the push host (of docker.push_url, else the
// registry host).
func (f *Format) hostsOf(baseURL string) (registry, push string) {
	if u, ok := urlOf(baseURL); ok {
		registry = u.Host
	}
	push = registry
	if u, ok := urlOf(PushURL.Get(f.h.Settings.Current())); ok {
		push = u.Host
	}
	return registry, push
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

// hosts answers GET /docker/hosts: the host of the registry and the host that pushes go to.
func (f *Format) hosts(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	registry, push := f.hostsOf(settings.BaseURL.Get(f.h.Settings.Current()))
	if registry == "" {
		_, registry = f.hostOf(r)
		if push == "" {
			push = registry
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"registry": registry, "push": push})
}

// location is the path of an object of name for the Location header.
func location(name, action, ref string) string {
	return "/v2/" + name + "/" + action + "/" + strings.TrimPrefix(ref, "/")
}
