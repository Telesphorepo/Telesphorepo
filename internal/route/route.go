// Package route is the route table of the server. Every module registers its routes through a
// Router of its own, each route with its Policy: the credentials it accepts, the second factor
// it needs, the server permission, and the repo it acts on. One middleware enforces the policy
// before the handler runs, so no handler checks credentials itself.
//
// The router refuses a path that the module does not own: a module owns the JSON API under
// /api/, the URL segment of each format it registers ("/maven/"), and the root prefixes it
// claims (e.g. "/v2/" for an OCI registry). The kernel adds GET /healthz and the web UI. Every
// route under /api/ sends its JSON answers gzip-compressed to a client that accepts gzip
// (httpx.GzipJSON).
package route

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Mux holds the routes of every module: the JSON API and every other path.
type Mux struct {
	root   *http.ServeMux
	claims map[string]string // owned prefix -> module ID
	rooted map[string]bool   // the first path segments of routes "/<segment>/{repo}/..." (RepoRooted)
	auth   *auth.Service
	access *access.Engine
	repos  *repos.Service
}

// NewMux returns an empty route table whose policies use the kernel services a, e and rs.
func NewMux(a *auth.Service, e *access.Engine, rs *repos.Service) *Mux {
	return &Mux{root: http.NewServeMux(), claims: map[string]string{}, rooted: map[string]bool{}, auth: a, access: e, repos: rs}
}

// RepoRooted reports whether a GET route of the table starts with "/<segment>/{repo}/", e.g. the
// routes of a format mounted under its name. Read it after every route is registered.
func (m *Mux) RepoRooted(segment string) bool { return m.rooted[segment] }

type domainKey struct{}

// WithRepoDomain marks r as a request that came to a repo domain (setting repo_domains). The
// policy of its route then reads no UI session cookie, and a 401 always carries the challenge.
func WithRepoDomain(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), domainKey{}, true))
}

// OnRepoDomain reports whether r came to a repo domain (WithRepoDomain).
func OnRepoDomain(r *http.Request) bool { v, _ := r.Context().Value(domainKey{}).(bool); return v }

// Claim gives prefix (a path that starts with "/", e.g. "/maven/" or "/install.sh") to module. It
// panics when another module claimed it, when it lies under /api/, or when one claim holds the
// other.
func (m *Mux) Claim(module, prefix string) {
	if !strings.HasPrefix(prefix, "/") || prefix == "/" || strings.HasPrefix(prefix, "/api/") || prefix == "/api" {
		panic(fmt.Sprintf("route: module %s cannot claim %q", module, prefix))
	}
	for p, owner := range m.claims {
		if strings.HasPrefix(p, prefix) || strings.HasPrefix(prefix, p) {
			panic(fmt.Sprintf("route: module %s claims %q, which overlaps %q of module %s", module, prefix, p, owner))
		}
	}
	m.claims[prefix] = module
}

// Router returns the router of module.
func (m *Mux) Router(module string) *Router { return &Router{mux: m, module: module} }

// Kernel registers a route of the kernel itself (GET /healthz, the web UI) on the root table.
func (m *Mux) Kernel(pattern string, h http.Handler) { m.root.Handle(pattern, h) }

// Handler returns the handler of every route.
func (m *Mux) Handler() http.Handler { return m.root }

// Router registers the routes of one module.
type Router struct {
	mux    *Mux
	module string
}

// Module returns the ID of the module that owns the router.
func (r *Router) Module() string { return r.module }

// Handle registers pattern (net/http ServeMux syntax) with policy p. It panics at startup when
// the path lies outside what the module owns, or when the policy breaks a rule of the server: a
// password, a read token or a device reaches only GET and HEAD routes without a server permission
// (Perm or SomeRepo), so every change of state and every permission route needs a UI session (and
// its second factor) or a publish session. A core bearer token can carry a password, a read token
// or a device, so a route that reads bearers has no server permission, and on a method other than
// GET or HEAD it writes or deletes in one repo, where only the open publish session of a bearer
// scope passes.
func (r *Router) Handle(pattern string, p Policy, h Handler) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		method, path = "", pattern
	}
	path = strings.TrimSpace(path)
	read := method == http.MethodGet || method == http.MethodHead
	if p.Accept&(auth.CredPassword|auth.CredReadToken|auth.CredDevice) != 0 {
		if !read {
			panic(fmt.Sprintf("route: %s accepts a password, a read token or a device on a method other than GET or HEAD", pattern))
		}
		if p.serverPerms() {
			panic(fmt.Sprintf("route: %s accepts a password, a read token or a device on a server permission route", pattern))
		}
	}
	if p.Repo != nil && p.Repo.Pick != nil && len(p.Repo.Ops) == 0 {
		panic(fmt.Sprintf("route: %s has Pick without Ops", pattern))
	}
	if p.URLToken != nil && !p.Bearer {
		panic(fmt.Sprintf("route: %s reads upload tokens without bearer tokens", pattern))
	}
	if p.Bearer {
		if p.serverPerms() {
			panic(fmt.Sprintf("route: %s reads bearer tokens on a server permission route", pattern))
		}
		if !read && p.Repo == nil {
			panic(fmt.Sprintf("route: %s reads bearer tokens on a method other than GET or HEAD without a repo write or delete", pattern))
		}
		if !read {
			for _, op := range p.Repo.ops() {
				if op != access.Write && op != access.Delete {
					panic(fmt.Sprintf("route: %s reads bearer tokens on a method other than GET or HEAD without a repo write or delete", pattern))
				}
			}
		}
	}
	if p.Repo != nil {
		for _, op := range p.Repo.ops() {
			if op == access.Write && p.Accept&^auth.CredPublish != 0 {
				panic(fmt.Sprintf("route: %s writes and accepts a credential other than a publish session", pattern))
			}
		}
	}
	var hh http.Handler = r.mux.enforce(p, h)
	api := strings.HasPrefix(path, "/api/")
	if api || p.Accept&(auth.CredSession|auth.CredSetupSession) != 0 {
		hh = http.NewCrossOriginProtection().Handler(hh)
	}
	if api {
		r.mux.root.Handle(pattern, httpx.GzipJSON(hh))
		return
	}
	for prefix, owner := range r.mux.claims {
		if owner == r.module && strings.HasPrefix(path, prefix) {
			if seg, rest, ok := strings.Cut(strings.TrimPrefix(path, "/"), "/"); ok && read && strings.HasPrefix(rest, "{repo}/") {
				r.mux.rooted[seg] = true
			}
			r.mux.root.Handle(pattern, hh)
			return
		}
	}
	panic(fmt.Sprintf("route: module %s registers %q outside the paths it owns", r.module, pattern))
}

// enforce returns the handler of a route: the policy steps, then h. Each refusal goes through
// the policy's ErrorWriter.
//
//  1. Identify the credentials the policy reads. A credential kind the policy does not accept is
//     refused: a publish session token with 403, a setup-only session with 403
//     second_factor_required, a password or read token with 401. On a write route a password or
//     read token goes on to the repo step, which answers that publishing needs a publish session.
//     A core bearer token is checked by the kind of its credential. On a repo route that accepts
//     publish sessions, a bearer of another kind goes on to the repo step, which lets it pass
//     only with the open publish session of its scope for the repo. A request to a repo domain
//     (OnRepoDomain) has no UI session: the cookie is not read.
//  2. A request without a credential passes only when the policy is Anonymous or names a repo.
//  3. The fresh second factor (FreshFactor).
//  4. The server permission, or the repository permission SomeRepo on at least one repo; then
//     every permission of AllPerms.
//  5. The repo: its name, or a redirect of it (repos.Service.ByName; the events of the request
//     then record the redirect, audit.WithRepoName), then access.Engine.Decide. A format read of a repo that not everyone reads
//     (access.Engine.Private) answers with Cache-Control: private, so a shared cache
//     in front of the server does not keep it. A public repo gets no Cache-Control here. A format
//     read keeps the members of a group that it reads for the request (repos.Remember).
//
// The audit actor of the caller (with the client address) is in the request context from step 1
// on, so the handler, the services it calls and a guard refusal record events with it.
func (m *Mux) enforce(p Policy, h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := &Call{}
		fail := func(d access.Decision) { p.refuse(w, r, d) }
		var op access.Op
		var needs []perm.ID
		if p.Repo != nil {
			var ok bool
			if op, needs, ok = p.Repo.pick(r); !ok {
				fail(access.Refuse(http.StatusInternalServerError, "internal", "internal error"))
				return
			}
		}
		if p.Accept != 0 || p.Bearer || p.TokenHeader != "" {
			// No UI session on a repo domain: the browser holds the cookie for the base URL host only.
			cookie := p.Accept&(auth.CredSession|auth.CredSetupSession) != 0 && !OnRepoDomain(r)
			pr, err := m.auth.Identify(r, auth.Read{
				Cookie: cookie, Bearer: p.Bearer, TokenHeader: p.TokenHeader,
				Device: p.Accept&auth.CredDevice != 0, URLToken: p.URLToken,
			})
			if err != nil {
				if d, ok := auth.RetryAfter(err); ok {
					setRetryAfter(w, d)
					fail(access.Refuse(http.StatusTooManyRequests, "rate_limited", auth.ErrRateLimited.Error()))
					return
				}
				switch {
				case errors.Is(err, auth.ErrPublishEnded), errors.Is(err, auth.ErrDeviceProofRefused) && op == access.Write:
					fail(m.access.PublishRequired())
				case errors.Is(err, auth.ErrBadCredentials):
					fail(access.Refuse(http.StatusUnauthorized, "credentials", auth.ErrBadCredentials.Error()))
				default:
					fail(access.Refuse(http.StatusInternalServerError, "internal", "internal error"))
				}
				return
			}
			c.P = pr
		}
		r = r.WithContext(audit.WithActor(r.Context(), auth.Actor(c.P, httpx.ClientIP(r))))
		// kindAtRepo defers the credential kind check to the repo step: a write answers there that
		// publishing needs a publish session, and a bearer token passes there with the open publish
		// session of its scope for the repo.
		kindAtRepo := false
		if pr := c.P; pr != nil && !pr.Anonymous() && !pr.Is(p.Accept) {
			switch {
			case p.Repo != nil && op == access.Write && !pr.Is(auth.CredSetupSession):
				kindAtRepo = true
			case pr.Scopes != nil && p.Repo != nil && p.Accept&auth.CredPublish != 0:
				kindAtRepo = true
			default:
				fail(kindRefusal(pr))
				return
			}
		}
		if c.P.Anonymous() && !p.Anonymous && p.Repo == nil {
			msg := auth.ErrSessionRequired.Error()
			if p.Accept&(auth.CredPassword|auth.CredReadToken) != 0 {
				msg = "authentication required"
			}
			fail(access.Refuse(http.StatusUnauthorized, "anonymous", msg))
			return
		}
		if p.FreshFactor {
			proof, err := m.auth.CheckProof(r.Context(), c.P, r.Header.Get(auth.ProofHeader))
			if err != nil {
				fail(proofRefusal(w, err))
				return
			}
			c.Proof = proof
		}
		if needs := p.needs(); (len(needs) > 0 || p.SomeRepo != "") && (c.P == nil || !c.P.Perms.HasAny(needs...)) {
			refused := access.Refuse(http.StatusForbidden, "permission", p.permMessage())
			if p.SomeRepo == "" {
				fail(refused)
				return
			}
			ids, all, err := m.auth.ManagedRepoIDs(r.Context(), c.P, p.SomeRepo)
			if err != nil {
				fail(access.Refuse(http.StatusInternalServerError, "internal", "internal error"))
				return
			}
			if !all && len(ids) == 0 {
				fail(refused)
				return
			}
			c.Managed = &Managed{All: all, IDs: ids}
		}
		if len(p.AllPerms) > 0 && (c.P == nil || !c.P.Perms.Contains(perm.SetOf(p.AllPerms...))) {
			fail(access.Refuse(http.StatusForbidden, "permission", p.allMessage()))
			return
		}
		if rule := p.Repo; rule != nil {
			name, err := rule.name(r)
			if err != nil {
				fail(access.Refuse(http.StatusBadRequest, "invalid", err.Error()))
				return
			}
			repo, err := m.repos.ByName(r.Context(), name)
			switch {
			case errors.Is(err, repos.ErrNotFound):
				fail(m.access.Missing(c.P, nil))
				return
			case err != nil:
				fail(access.Refuse(http.StatusInternalServerError, "internal", "internal error"))
				return
			case rule.Format != "" && repo.Format != rule.Format:
				fail(m.access.Missing(c.P, repo))
				return
			}
			// A bearer token of another credential kind reads or reads metadata only with the open
			// publish session of its scope; on a write or a delete, the engine answers from its scope.
			if kindAtRepo && op != access.Write && op != access.Delete {
				if sc := c.P.ScopeOf(repo.ID); sc == nil || sc.Open == nil {
					fail(kindRefusal(c.P))
					return
				}
			}
			d := m.access.Decide(r.Context(), r, c.P, repo, op, needs...)
			if !d.Allowed {
				fail(d)
				return
			}
			c.Repo, c.RepoPerms = repo, d.Perms
			if repo.Name != name {
				// The request named a redirect of the repo: its events record that name.
				r = r.WithContext(audit.WithRepoName(r.Context(), repo.ID, name))
			}
			if rule.Format != "" && op == access.Read {
				// A format read reads the members of a group once: here, and again in the read path.
				r = r.WithContext(repos.Remember(r.Context()))
				private, err := m.access.Private(r.Context(), repo)
				if err != nil {
					fail(access.Refuse(http.StatusInternalServerError, "internal", "internal error"))
					return
				}
				if private {
					w.Header().Set("Cache-Control", "private")
				}
			}
		}
		h(w, r, c)
	}
}

// kindRefusal is the answer to a credential kind that the route does not accept.
func kindRefusal(pr *auth.Principal) access.Decision {
	switch {
	case pr.Is(auth.CredSetupSession):
		return access.Refuse(http.StatusForbidden, "second_factor", auth.ErrNoSecondFactor.Error())
	case pr.Is(auth.CredPublish):
		return access.Refuse(http.StatusForbidden, "publish_session", "a publish session token only publishes")
	}
	return access.Refuse(http.StatusUnauthorized, "session", auth.ErrSessionRequired.Error())
}

// proofRefusal maps a failed fresh second-factor check.
func proofRefusal(w http.ResponseWriter, err error) access.Decision {
	if d, ok := auth.RetryAfter(err); ok {
		setRetryAfter(w, d)
		return access.Refuse(http.StatusTooManyRequests, "rate_limited", auth.ErrRateLimited.Error())
	}
	switch {
	case errors.Is(err, auth.ErrNoSecondFactor):
		return access.Refuse(http.StatusForbidden, "second_factor", auth.ErrNoSecondFactor.Error())
	case errors.Is(err, auth.ErrPasskeysUnavailable):
		return access.Refuse(http.StatusConflict, "passkeys", err.Error())
	case errors.Is(err, auth.ErrGrantRequired), errors.Is(err, auth.ErrGrantUsed):
		return access.Refuse(http.StatusUnauthorized, "proof", strings.TrimPrefix(err.Error(), auth.ErrBadCredentials.Error()+": "))
	case errors.Is(err, auth.ErrBadCredentials):
		return access.Refuse(http.StatusUnauthorized, "proof", "second factor did not verify")
	case errors.Is(err, auth.ErrInvalid):
		return access.Refuse(http.StatusBadRequest, "proof", strings.TrimPrefix(err.Error(), auth.ErrInvalid.Error()+": "))
	}
	return access.Refuse(http.StatusInternalServerError, "internal", "internal error")
}
