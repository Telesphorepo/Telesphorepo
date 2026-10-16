package route

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Policy is what a route needs from its caller. The router checks it at registration and the
// middleware enforces it on every request (see Mux.enforce).
type Policy struct {
	// Accept are the credential kinds that pass. 0 with Anonymous is a public route: the
	// middleware reads no credential, and the handler may read one itself.
	Accept auth.Cred
	// Anonymous lets a request without a credential through to the repo step or the handler.
	Anonymous bool
	// Bearer reads core bearer tokens (auth.Service.IssueBearer) on this route. A bearer token
	// carries the credential it was issued for, limited to its scopes. It passes when Accept
	// holds that credential, or on a repo route that accepts CredPublish when its scope of the
	// repo has an open publish session.
	Bearer bool
	// TokenHeader reads a token of a registered kind from this header too, e.g. "X-NuGet-ApiKey",
	// or "Authorization" for a raw token without a scheme.
	TokenHeader string
	// FreshFactor needs a fresh second factor in the header auth.ProofHeader (Call.Proof).
	FreshFactor bool
	// Perm is the server permission the caller holds ("" for none).
	Perm perm.ID
	// AnyPerm passes a caller who holds one of these server permissions.
	AnyPerm []perm.ID
	// SomeRepo is a repository permission that passes in place of Perm and AnyPerm when the
	// caller holds it on at least one repo, e.g. repo.settings to read the list of signing keys.
	// The middleware puts those repos in Call.Managed.
	SomeRepo perm.ID
	// Repo is set when the route acts on one repo.
	Repo *RepoRule
	// Errors writes a refusal. nil: JSON {"error": message} (httpx.Error). A format with its own
	// error format (e.g. the OCI error JSON) sets it.
	Errors ErrorWriter
	// Challenge sets the WWW-Authenticate header of a 401. nil: a Basic challenge on a route
	// outside /api/, none under /api/ and none for a request of the web UI (auth.UIHeader).
	Challenge Challenger
}

// RepoRule is the repo a route acts on and what it does there.
type RepoRule struct {
	// Name returns the repo name of the request. nil: the path value {repo}, else {name}.
	Name func(r *http.Request) (string, error)
	// Format is the format the repo must be of ("" for any); a repo of another format is 404.
	Format string
	// Op is the operation (access.Read, access.Write, ...).
	Op access.Op
	// Perm is the repository permission the operation needs. "" with access.Meta, and without
	// AnyPerm, is any repository permission.
	Perm perm.ID
	// AnyPerm needs one of these repository permissions.
	AnyPerm []perm.ID
}

// needs returns the repository permissions of the rule: Perm, or one of AnyPerm.
func (rr *RepoRule) needs() []perm.ID {
	if rr.Perm != "" {
		return append([]perm.ID{rr.Perm}, rr.AnyPerm...)
	}
	return rr.AnyPerm
}

func (rr *RepoRule) name(r *http.Request) (string, error) {
	if rr.Name != nil {
		return rr.Name(r)
	}
	if n := r.PathValue("repo"); n != "" {
		return n, nil
	}
	if n := r.PathValue("name"); n != "" {
		return n, nil
	}
	return "", errors.New("no repository in the path")
}

// Call is what the middleware found out about a request that passed the policy.
type Call struct {
	P         *auth.Principal // nil: anonymous
	Repo      *repos.Repo     // nil without a RepoRule
	RepoPerms perm.Set        // the caller's repository permissions on Repo
	Proof     *auth.Proof     // the fresh second factor (FreshFactor only)
	// Managed are the repos where the caller holds Policy.SomeRepo. It is set only when the
	// route has SomeRepo and the caller passed through it, not through Perm.
	Managed *Managed
}

// Managed is a set of repos: every repo (a holder of All repositories), or the repos of IDs.
type Managed struct {
	All bool
	IDs map[int64]bool
}

// Has reports whether the set holds the repo id.
func (m *Managed) Has(id int64) bool { return m.All || m.IDs[id] }

// Handler serves a request that passed its policy.
type Handler func(w http.ResponseWriter, r *http.Request, c *Call)

// ErrorWriter writes the refusal d of a request. d.Status and d.Message are set.
type ErrorWriter func(w http.ResponseWriter, r *http.Request, d access.Decision)

// Challenger sets the WWW-Authenticate header of a 401 answer.
type Challenger func(w http.ResponseWriter, r *http.Request)

// BasicChallenge is the challenge of the format routes.
func BasicChallenge(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("WWW-Authenticate", `Basic realm="telesphorepo"`)
}

// refuse writes the refusal d: the challenge of a 401, the Allow header of a write to a
// read-only repo, then the ErrorWriter.
func (p Policy) refuse(w http.ResponseWriter, r *http.Request, d access.Decision) {
	if d.Status == http.StatusUnauthorized && r.Header.Get(auth.UIHeader) != "1" {
		switch {
		case p.Challenge != nil:
			p.Challenge(w, r)
		case !strings.HasPrefix(r.URL.Path, "/api/"):
			BasicChallenge(w, r)
		}
	}
	if d.Code == access.CodeReadOnlyKind {
		w.Header().Set("Allow", "GET, HEAD")
	}
	if p.Errors != nil {
		p.Errors(w, r, d)
		return
	}
	httpx.Error(w, d.Status, d.Message)
}

// needs returns the server permissions of the policy: Perm, or AnyPerm.
func (p Policy) needs() []perm.ID {
	if p.Perm != "" {
		return append([]perm.ID{p.Perm}, p.AnyPerm...)
	}
	return p.AnyPerm
}

// permMessage is the refusal message of a caller without the server permission and without
// SomeRepo, e.g. "permission required: Signing keys, or Edit settings on a repository".
func (p Policy) permMessage() string {
	g := perm.Current()
	var parts []string
	if ids := p.needs(); len(ids) > 0 {
		labels := make([]string, len(ids))
		for i, id := range ids {
			labels[i] = g.Label(id)
		}
		parts = append(parts, strings.Join(labels, " or "))
	}
	if p.SomeRepo != "" {
		parts = append(parts, g.Label(p.SomeRepo)+" on a repository")
	}
	return "permission required: " + strings.Join(parts, ", or ")
}

func setRetryAfter(w http.ResponseWriter, d time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(d.Seconds())))))
}

// Presets of common policies.
var (
	// Public reads no credential.
	Public = Policy{Anonymous: true}
	// UI needs a full UI session.
	UI = Policy{Accept: auth.CredSession}
	// Setup needs a UI session, setup-only or full: the routes that a person without the second
	// factor they need may use (their account, sign-out, the second-factor setup).
	Setup = Policy{Accept: auth.CredSession | auth.CredSetupSession}
	// AnyUser needs a password, a read token or a UI session (GET and HEAD routes only).
	AnyUser = Policy{Accept: auth.CredPassword | auth.CredReadToken | auth.CredSession}
)

// UIPerm needs a UI session of a holder of the server permission id.
func UIPerm(id perm.ID) Policy { return Policy{Accept: auth.CredSession, Perm: id} }

// UIRepo needs a UI session of a holder of one of the repository permissions ids on repo
// {name}, for op (access.Manage for a change of the repo, access.Meta to read its settings).
// No ids with access.Meta is any repository permission.
func UIRepo(op access.Op, ids ...perm.ID) Policy {
	return Policy{Accept: auth.CredSession, Repo: &RepoRule{Op: op, AnyPerm: ids}}
}

// RepoRead is a JSON read of repo {name} that needs download: a password, a read token, a UI
// session or anonymous on a repo with anonymous read.
func RepoRead() Policy {
	return Policy{Accept: auth.CredPassword | auth.CredReadToken | auth.CredSession, Anonymous: true,
		Repo: &RepoRule{Op: access.Read, Perm: perm.RepoDownload}}
}

// FormatRead is a download from a repo of format: anonymous, a password, a read token, a UI
// session or a publish session.
func FormatRead(format string) Policy {
	return Policy{Accept: auth.CredPassword | auth.CredReadToken | auth.CredSession | auth.CredPublish, Anonymous: true,
		Repo: &RepoRule{Format: format, Op: access.Read, Perm: perm.RepoDownload}}
}

// FormatWrite is a write to a repo of format: a publish session of the repo only.
func FormatWrite(format string) Policy {
	return Policy{Accept: auth.CredPublish, Repo: &RepoRule{Format: format, Op: access.Write, Perm: perm.RepoPublish}}
}
