package docker

import (
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/publish"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

const (
	// tokenTTL is the longest life of a registry bearer token.
	tokenTTL = 15 * time.Minute
	// minTokenTTL is the shortest: clients cache a token for at least a minute.
	minTokenTTL = time.Minute
	// maxTokenForm bounds the form of a POST to the token endpoint.
	maxTokenForm = 64 << 10
)

// Kinds of the credential of a token request.
const (
	credAnonymous = iota
	credCIToken
	credDevice  // a device proof of a token exchange nonce (the credential helper)
	credAccount // a password, a read token or a publish session token
)

// ciUserPrefix starts the user name of a CI token from the credential helper of the CLI:
// "telesphorepo-ci@<commit>". The build record keeps the commit when the token names none.
const ciUserPrefix = "telesphorepo-ci@"

// tokenRequest is what a token request asks for.
type tokenRequest struct {
	user, pass string
	kind       int
	device     *publish.DeviceProof // credDevice only
	scopes     []scopeRequest       // in request order, one per repo
}

// scopeRequest is the request of one repo: pull, push and delete.
type scopeRequest struct {
	repo               string
	pull, push, delete bool
}

// readTokenRequest reads a GET with Basic credentials and the query, or a POST form
// (grant_type password with username and password, or refresh_token with refresh_token, which is
// read as a password).
func readTokenRequest(w http.ResponseWriter, r *http.Request) (*tokenRequest, *ociError) {
	tr := &tokenRequest{}
	var scopes []string
	for _, s := range r.URL.Query()["scope"] {
		scopes = append(scopes, strings.Fields(s)...)
	}
	switch r.Method {
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxTokenForm)
		if err := r.ParseForm(); err != nil {
			return nil, ociErr(http.StatusBadRequest, codeUnsupported, "the token request is not a valid form")
		}
		switch r.PostForm.Get("grant_type") {
		case "password":
			tr.user, tr.pass = r.PostForm.Get("username"), r.PostForm.Get("password")
		case "refresh_token":
			tr.pass = r.PostForm.Get("refresh_token")
		default:
			return nil, ociErr(http.StatusBadRequest, codeUnsupported, "unsupported grant_type: use password or refresh_token")
		}
		scopes = append(scopes, strings.Fields(r.PostForm.Get("scope"))...)
		if tr.pass == "" {
			return nil, ociErr(http.StatusUnauthorized, codeUnauthorized, auth.ErrBadCredentials.Error())
		}
	default:
		if h := r.Header.Get("Authorization"); h != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || pass == "" {
				return nil, ociErr(http.StatusUnauthorized, codeUnauthorized, auth.ErrBadCredentials.Error())
			}
			tr.user, tr.pass = user, pass
		}
	}
	switch {
	case tr.pass == "":
		tr.kind = credAnonymous
	case strings.HasPrefix(tr.pass, auth.DeviceProofPrefix):
		tr.kind = credDevice
		tr.device = publish.ParseDeviceProof(tr.pass)
	case isJWT(tr.pass):
		tr.kind = credCIToken
	default:
		tr.kind = credAccount
	}
	tr.scopes = parseScopes(scopes)
	return tr, nil
}

// isJWT reports whether s has the form of a JWT: three base64url parts, the first a JSON object.
func isJWT(s string) bool {
	parts := strings.Split(s, ".")
	return len(parts) == 3 && strings.HasPrefix(s, "eyJ") && parts[1] != "" && parts[2] != ""
}

// parseScopes reads "repository:<name>:<actions>" scopes into one request per repo, in the order
// of the first scope of each repo. Other scopes (e.g. registry:catalog:*) are dropped: the
// catalog filters by what the credential reads.
func parseScopes(scopes []string) []scopeRequest {
	var out []scopeRequest
	for _, s := range scopes {
		typ, rest, ok := strings.Cut(s, ":")
		if !ok || typ != "repository" {
			continue
		}
		i := strings.LastIndexByte(rest, ':')
		if i <= 0 {
			continue
		}
		repo, _, _ := strings.Cut(rest[:i], "/")
		j := slices.IndexFunc(out, func(x scopeRequest) bool { return x.repo == repo })
		if j < 0 {
			out = append(out, scopeRequest{repo: repo})
			j = len(out) - 1
		}
		for _, a := range strings.Split(rest[i+1:], ",") {
			switch strings.TrimSpace(a) {
			case "pull":
				out[j].pull = true
			case "push":
				out[j].push = true
			case "delete":
				out[j].delete = true
			case "*":
				out[j].pull, out[j].push, out[j].delete = true, true, true
			}
		}
	}
	return out
}

// tokenAnswer is the answer of the token endpoint.
type tokenAnswer struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	IssuedAt    string `json:"issued_at"`
}

func retryAfter(d time.Duration) string {
	return strconv.Itoa(max(1, int(math.Ceil(d.Seconds()))))
}

// token answers GET and POST /docker/token: a core bearer token with the scopes the credential
// may have. Pulls: what the credential reads (a CI token reads what anonymous reads, a device
// proof what the device's user reads). Push and delete: for at most one repo, the first in
// request order that asks for them, and only with a publish session: the session of a publish
// session token (its own repo), or the session that a CI identity token or a device proof mints.
// A password or a read token gets no push; the write then answers that publishing needs a
// publish session.
func (f *Format) token(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	ctx := r.Context()
	if d := f.h.Auth.Blocked(r); d > 0 {
		w.Header().Set("Retry-After", retryAfter(d))
		writeOCI(w, ociErr(http.StatusTooManyRequests, codeTooManyRequests, auth.ErrRateLimited.Error()))
		return
	}
	tr, oe := readTokenRequest(w, r)
	if oe != nil {
		writeOCI(w, oe)
		return
	}
	var cred *auth.Principal // the credential the bearer token carries; nil: anonymous
	if tr.kind == credAccount {
		p, err := f.h.Auth.Authenticate(r, tr.user, tr.pass)
		if d, ok := auth.RetryAfter(err); ok {
			w.Header().Set("Retry-After", retryAfter(d))
			writeOCI(w, ociErr(http.StatusTooManyRequests, codeTooManyRequests, auth.ErrRateLimited.Error()))
			return
		}
		if errors.Is(err, auth.ErrBadCredentials) {
			writeOCI(w, ociErr(http.StatusUnauthorized, codeUnauthorized, err.Error()))
			return
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		cred = p
	}

	// The repos of the request, and the one that gets push or delete.
	type asked struct {
		scopeRequest
		repo *repos.Repo
	}
	var all []asked
	pushAt := -1
	for _, s := range tr.scopes {
		repo, err := f.h.Repos.ByName(ctx, s.repo)
		if errors.Is(err, repos.ErrNotFound) {
			continue
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		if repo.Format != name {
			continue
		}
		all = append(all, asked{s, repo})
		if pushAt < 0 && (s.push || s.delete) {
			pushAt = len(all) - 1
		}
	}

	var session *auth.PublishSession
	switch tr.kind {
	case credCIToken:
		if pushAt >= 0 && all[pushAt].repo.Kind == repos.KindHosted {
			// The credential helper of the CLI names the commit of the job in the user name.
			revision, _ := strings.CutPrefix(tr.user, ciUserPrefix)
			ps, err := f.minter.Mint(ctx, r, publish.MintCred{CIToken: tr.pass, Revision: revision}, all[pushAt].repo)
			if err != nil {
				f.mintFailed(w, r, err, http.StatusForbidden)
				return
			}
			if cred, err = f.h.Auth.PublishPrincipal(ctx, ps, 0); err != nil {
				fail(w, r, err)
				return
			}
			session = ps
		} else if err := f.minter.Check(ctx, r, publish.MintCred{CIToken: tr.pass}); err != nil {
			f.mintFailed(w, r, err, http.StatusUnauthorized)
			return
		}
	case credDevice:
		// The bearer token carries the device: it reads what the device's user reads, and it
		// writes only through the session that the proof mints for the push repo.
		if tr.device == nil {
			f.h.Auth.Failed(r, "")
			writeOCI(w, ociErr(http.StatusUnauthorized, codeUnauthorized, auth.ErrBadCredentials.Error()))
			return
		}
		mc := publish.MintCred{Device: tr.device}
		if pushAt >= 0 && all[pushAt].repo.Kind == repos.KindHosted {
			ps, err := f.minter.Mint(ctx, r, mc, all[pushAt].repo)
			if err != nil {
				f.mintFailed(w, r, err, http.StatusForbidden)
				return
			}
			session = ps
		} else if err := f.minter.Check(ctx, r, mc); err != nil {
			f.mintFailed(w, r, err, http.StatusUnauthorized)
			return
		}
		p, err := f.h.Auth.DevicePrincipal(ctx, tr.device.DeviceID)
		if errors.Is(err, auth.ErrBadCredentials) {
			f.h.Auth.Failed(r, "")
			writeOCI(w, ociErr(http.StatusUnauthorized, codeUnauthorized, err.Error()))
			return
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		cred = p
	}
	if cred.Is(auth.CredPublish) && session == nil {
		session = cred.Publish
	}

	var scopes []auth.Scope
	for i, a := range all {
		sc := auth.Scope{Repo: a.repo.Name}
		if a.pull || i == pushAt {
			ok, err := f.h.Access.Allows(ctx, r, cred, a.repo, access.Read, perm.RepoDownload)
			if err != nil {
				fail(w, r, err)
				return
			}
			if ok {
				sc.Ops = append(sc.Ops, access.Read)
			}
		}
		if i == pushAt && session != nil && session.RepoID == a.repo.ID {
			if a.push {
				sc.Ops = append(sc.Ops, access.Write)
			}
			if a.delete && (tr.kind == credAccount || tr.kind == credDevice) {
				sc.Ops = append(sc.Ops, access.Delete)
			}
			sc.Session, sc.Open = session.ID, session
		}
		if len(sc.Ops) > 0 {
			scopes = append(scopes, sc)
		}
	}
	ttl := tokenTTL
	if session != nil && !session.Expires.IsZero() {
		ttl = min(ttl, time.Until(session.Expires))
	}
	ttl = max(ttl, minTokenTTL)
	now := time.Now()
	tok, exp, err := f.h.Auth.IssueBearer(cred, scopes, ttl)
	if err != nil {
		fail(w, r, err)
		return
	}
	in := int(exp.Sub(now).Round(time.Second) / time.Second)
	httpx.WriteJSON(w, http.StatusOK, tokenAnswer{Token: tok, AccessToken: tok, ExpiresIn: in, IssuedAt: now.UTC().Format(time.RFC3339)})
}

// mintFailed answers a CI token or a device proof that a check (status 401) or a mint (status 403) refused, with
// the message of the refusal. A credential that did not verify counts as a failed attempt of the
// client address.
func (f *Format) mintFailed(w http.ResponseWriter, r *http.Request, err error, status int) {
	code := codeDenied
	if status == http.StatusUnauthorized {
		code = codeUnauthorized
	}
	var refused *publish.RefusedError
	switch {
	case errors.As(err, &refused):
		writeOCI(w, ociErr(http.StatusForbidden, codeDenied, refused.D.Message))
	case errors.Is(err, publish.ErrDenied), errors.Is(err, publish.ErrInvalid):
		f.h.Auth.Failed(r, "")
		writeOCI(w, ociErr(status, code, err.Error()))
	case errors.Is(err, publish.ErrForbidden), errors.Is(err, publish.ErrNotFound), errors.Is(err, publish.ErrConflict):
		writeOCI(w, ociErr(http.StatusForbidden, codeDenied, err.Error()))
	default:
		fail(w, r, err)
	}
}
