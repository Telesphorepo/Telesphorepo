package access

import (
	"context"
	"net/http"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// Op is an operation on one repo.
type Op = auth.Op

const (
	Meta   = auth.OpMeta   // read the repo record: any repository permission, no guard
	Read   = auth.OpRead   // download
	Write  = auth.OpWrite  // store a file (format write, upload)
	Delete = auth.OpDelete // remove files
	Manage = auth.OpManage // change repo settings, grants, rules
	Mint   = auth.OpMint   // mint a publish session for the repo
)

// Refusal codes of a Decision.
const (
	CodeAnonymous      = "anonymous"
	CodeNotFound       = "not_found"
	CodePermission     = "permission"
	CodeReadOnlyKind   = "read_only_kind"
	CodeOperation      = "operation"
	CodePublishSession = "publish_session"
	CodeNetwork        = "network"
	CodeScope          = "scope"
)

// Decision is the answer of the engine. A refusal has the HTTP status (401, 403, 404, 405 or
// 409), a code and a message for the caller.
type Decision struct {
	Allowed bool
	Status  int
	Code    string
	Message string
	Detail  map[string]any
	// Perms are the caller's repository permissions on the repo (allowed or not).
	Perms perm.Set
	// Audit is the event of a guard refusal; Decide records it, Allows drops it.
	Audit *audit.Event
}

// Refuse returns a refusal.
func Refuse(status int, code, msg string) Decision {
	return Decision{Status: status, Code: code, Message: msg}
}

// Guard is an extra rule on repo access, e.g. the IP rules of the network module. Guards never
// see Meta or Manage: a holder of the settings or access permission can open the repo and fix
// the rules that refuse them.
type Guard interface {
	Name() string
	// Check returns nil when the guard lets p do op on repo through r, else the refusal (403 with
	// an audit event in Audit).
	Check(r *http.Request, p *auth.Principal, repo *repos.Repo, op Op) *Decision
	// Describe says how the guard limits op on repo, for the access report.
	Describe(repo *repos.Repo, op Op) GuardReport
	// Refused returns the repos whose downloads the guard refuses to r; search leaves them out.
	Refused(r *http.Request) map[int64]bool
}

// ReadRestricter is a Guard that can say cheaply, from memory, whether it limits who reads repo:
// a read of such a repo is not the same for everyone, so a shared cache must not keep it.
type ReadRestricter interface {
	RestrictsRead(repo *repos.Repo) bool
}

// GuardReport is what a guard says about one operation of a repo in the access report.
type GuardReport struct {
	Restricted bool           `json:"restricted"`
	Summary    string         `json:"summary"`
	Detail     map[string]any `json:"detail"`
}

// Engine is the one policy engine of repo access: the route policies, the publish-session mint,
// the group read path and the access report all ask it.
type Engine struct {
	q      *dbq.Queries
	repos  *repos.Service
	auth   *auth.Service
	audit  *audit.Log
	set    settings.Provider
	ops    *ops.Service
	guards []Guard

	pipelines Pipelines      // the CI rules of the report; set before the server serves
	notes     []DownloadNote // the notes of the report's downloads; set before the server serves
}

// New returns the engine. os gives the message of a repo's operation.
func New(d *db.DB, rs *repos.Service, a *auth.Service, au *audit.Log, set settings.Provider, os *ops.Service) *Engine {
	return &Engine{q: dbq.New(d.R), repos: rs, auth: a, audit: au, set: set, ops: os}
}

// AddGuard adds a guard, in module start order. It runs before the server serves.
func (e *Engine) AddGuard(g Guard) { e.guards = append(e.guards, g) }

// RepoPerms returns what p may do on repo. A publish session holds, on its own repo, download
// and publish (a CI session) or the user's current download, publish and delete (a device or
// manual session); on another repo it holds what anonymous holds. Every other credential holds
// its grants (every permission with All repositories; a device holds the grants of its user), and
// anyone holds download on a repo with anonymous read.
func (e *Engine) RepoPerms(ctx context.Context, p *auth.Principal, repo *repos.Repo) (perm.Set, error) {
	if p.Is(auth.CredPublish) {
		if p.Publish.RepoID != repo.ID {
			return e.auth.RepoPerms(ctx, nil, repo)
		}
		if p.Publish.Via == "oidc" {
			return perm.SetOf(perm.RepoDownload, perm.RepoPublish), nil
		}
		set, err := e.auth.RepoPerms(ctx, p, repo)
		return set.Intersect(perm.SetOf(perm.RepoDownload, perm.RepoPublish, perm.RepoDelete)), err
	}
	if p.Anonymous() {
		return e.auth.RepoPerms(ctx, nil, repo)
	}
	return e.auth.RepoPerms(ctx, p, repo)
}

// ReadOnly is the answer to a write to a proxy or a group repo.
func ReadOnly(repo *repos.Repo) string {
	if repo.Kind == repos.KindGroup {
		return "A group is read-only. Publish to one of its repositories."
	}
	return "A proxy is read-only."
}

// readOnly reports whether the kind of repo refuses op: a proxy and a group take no writes and
// no mints, and a group has no files of its own to delete. A proxy's cached files can be deleted.
func readOnly(repo *repos.Repo, op Op) bool {
	switch op {
	case Write, Mint:
		return repo.Kind != repos.KindHosted
	case Delete:
		return repo.Kind == repos.KindGroup
	}
	return false
}

// Missing is the answer to p for a repo that does not exist, or that is not of the format a
// route serves (repo then is that repo). Anonymous gets 401, so the answer does not tell that a
// private repo exists; anyone else 404.
func (e *Engine) Missing(p *auth.Principal, repo *repos.Repo) Decision {
	if p.Anonymous() && (repo == nil || !repo.AnonymousRead) {
		return Refuse(http.StatusUnauthorized, CodeAnonymous, "authentication required")
	}
	return Refuse(http.StatusNotFound, CodeNotFound, "repo not found")
}

// Decide decides whether p may do op on repo through r with one of the repository permissions in
// need (none: any permission, for Meta). In order: the permissions of p, the repo kind (405 for a
// write to a proxy or a group), the permission (401 for anonymous, 401 for a write without an
// open publish session of the repo, else 403), the operation of the repo (409), then the guards
// (Read, Write, Delete and Mint only). A guard refusal is recorded in the audit log.
func (e *Engine) Decide(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, op Op, need ...perm.ID) Decision {
	d := e.decide(ctx, r, p, repo, op, need)
	if d.Audit != nil {
		_ = e.audit.Record(ctx, *d.Audit)
	}
	return d
}

// Allows is Decide without the audit event of a guard: a group skips a member that p may not
// read.
func (e *Engine) Allows(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, op Op, need ...perm.ID) (bool, error) {
	d := e.decide(ctx, r, p, repo, op, need)
	if d.Status == http.StatusInternalServerError {
		return false, errInternal
	}
	return d.Allowed, nil
}

// GroupMembers returns the members of group that p reads through r, in read order: a group read
// skips a member that p may not read. p read the group already. A core bearer token reads each
// member with the rights of its credential (auth.Principal.InGroup), since its scopes name the
// group only.
func (e *Engine) GroupMembers(ctx context.Context, r *http.Request, p *auth.Principal, group *repos.Repo) ([]*repos.Repo, error) {
	members, err := e.repos.MemberRepos(ctx, group)
	if err != nil {
		return nil, err
	}
	q := p.InGroup(group.Name)
	out := members[:0]
	for _, m := range members {
		ok, err := e.Allows(ctx, r, q, m, Read, perm.RepoDownload)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// Private reports whether a read of repo can serve a file that not everyone reads: repo has no
// anonymous read or a guard limits its reads (a read IP rule), or it is a group with such a
// member. A format read of such a repo answers with Cache-Control: private, so a shared cache (a
// CDN) does not keep it.
func (e *Engine) Private(ctx context.Context, repo *repos.Repo) (bool, error) {
	if e.restricted(repo) {
		return true, nil
	}
	if repo.Kind != repos.KindGroup {
		return false, nil
	}
	members, err := e.repos.MemberRepos(ctx, repo)
	if err != nil {
		return false, err
	}
	for _, m := range members {
		if e.restricted(m) {
			return true, nil
		}
	}
	return false, nil
}

// Guarded reports, from memory, whether a guard can refuse a read of repo itself (not of its
// members), e.g. a read IP rule: such a read depends on the client, so the edge Worker leaves it to
// the server. A guard that is not a ReadRestricter counts as guarding every repo.
func (e *Engine) Guarded(repo *repos.Repo) bool {
	for _, g := range e.guards {
		rr, ok := g.(ReadRestricter)
		if !ok || rr.RestrictsRead(repo) {
			return true
		}
	}
	return false
}

// restricted reports whether repo has no anonymous read or a guard limits its reads.
func (e *Engine) restricted(repo *repos.Repo) bool {
	if !repo.AnonymousRead {
		return true
	}
	for _, g := range e.guards {
		if rr, ok := g.(ReadRestricter); ok && rr.RestrictsRead(repo) {
			return true
		}
	}
	return false
}

type internalError struct{}

func (internalError) Error() string { return "access: internal error" }

var errInternal error = internalError{}

func (e *Engine) decide(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, op Op, need []perm.ID) Decision {
	if p != nil && p.Scopes != nil {
		sc := p.ScopeOf(repo.Name)
		check := op
		if check == Meta {
			check = Read
		}
		if sc == nil || !sc.Allows(check) {
			return e.scopeRefusal(ctx, r, p, sc, repo, op)
		}
		p = p.For(repo.Name)
	}
	set, err := e.RepoPerms(ctx, p, repo)
	if err != nil {
		return Refuse(http.StatusInternalServerError, "internal", "internal error")
	}
	refuse := func() Decision {
		d := e.refusal(p, repo, op, need)
		d.Perms = set
		return d
	}
	if readOnly(repo, op) {
		if set.Has(perm.RepoDownload) {
			// A format write or delete names a method the repo kind does not take (405); a mint is
			// an API request whose method is fine, so the repo's kind is the conflict (409).
			status := http.StatusMethodNotAllowed
			if op == Mint {
				status = http.StatusConflict
			}
			d := Refuse(status, CodeReadOnlyKind, ReadOnly(repo))
			d.Perms = set
			return d
		}
		return refuse()
	}
	if op == Write && !(p.Is(auth.CredPublish) && p.Publish.RepoID == repo.ID) {
		return refuse()
	}
	if (len(need) == 0 && set.Empty()) || (len(need) > 0 && !set.HasAny(need...)) {
		return refuse()
	}
	if (op == Write || op == Delete || op == Mint) && repo.Op != nil {
		return Decision{Status: http.StatusConflict, Code: CodeOperation, Message: e.ops.Message(repo.Op), Perms: set}
	}
	if op == Read || op == Write || op == Delete || op == Mint {
		for _, g := range e.guards {
			if d := g.Check(r, p, repo, op); d != nil {
				d.Perms = set
				return *d
			}
		}
	}
	return Decision{Allowed: true, Perms: set}
}

// scopeRefusal is the answer to a core bearer token p whose scope sc of repo (nil: none) lacks
// op. A guard that refuses p is the answer first (403, recorded), so the caller learns the real
// reason. A token that reads the repo gets the answer of its credential: a write or delete on a
// proxy or a group is the read-only 405, and a write elsewhere needs a publish session (401).
// Any other case is the code CodeScope (401): the token does not hold the operation.
func (e *Engine) scopeRefusal(ctx context.Context, r *http.Request, p *auth.Principal, sc *auth.Scope, repo *repos.Repo, op Op) Decision {
	if op == Read || op == Write || op == Delete || op == Mint {
		for _, g := range e.guards {
			if d := g.Check(r, p, repo, op); d != nil {
				return *d
			}
		}
	}
	if sc != nil && sc.Allows(Read) {
		switch {
		case (op == Write || op == Delete) && repo.Kind != repos.KindHosted:
			return Refuse(http.StatusMethodNotAllowed, CodeReadOnlyKind, ReadOnly(repo))
		case op == Write:
			return e.PublishRequired()
		}
	}
	return Refuse(http.StatusUnauthorized, CodeScope, "the token does not allow this operation on "+repo.Name)
}

// refusal is the answer to p without the permission for op on repo.
func (e *Engine) refusal(p *auth.Principal, repo *repos.Repo, op Op, need []perm.ID) Decision {
	switch {
	case op == Write && !(p.Is(auth.CredPublish) && p.Publish.RepoID == repo.ID):
		return e.PublishRequired()
	case p.Anonymous():
		return Refuse(http.StatusUnauthorized, CodeAnonymous, "authentication required")
	}
	msg := "forbidden"
	if len(need) > 0 {
		g := perm.Current()
		labels := make([]string, len(need))
		for i, id := range need {
			labels[i] = g.Label(id)
		}
		msg = "permission required on " + repo.Name + ": " + strings.Join(labels, " or ")
	}
	return Refuse(http.StatusForbidden, CodePermission, msg)
}

// PublishRequired is the answer to a write without an open publish session of the repo.
func (e *Engine) PublishRequired() Decision {
	base := strings.TrimSuffix(settings.BaseURL.Get(e.set()), "/")
	return Refuse(http.StatusUnauthorized, CodePublishSession, "publishing needs a publish session: "+base+"/#/settings/tokens")
}

// RefusedRepos returns the repos whose downloads a guard refuses to r.
func (e *Engine) RefusedRepos(r *http.Request) map[int64]bool {
	var out map[int64]bool
	for _, g := range e.guards {
		for id := range g.Refused(r) {
			if out == nil {
				out = map[int64]bool{}
			}
			out[id] = true
		}
	}
	return out
}
