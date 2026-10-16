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

	pipelines Pipelines // the CI rules of the report; set before the server serves
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
// its grants (every permission with All repositories), and anyone holds download on a repo with
// anonymous read.
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
			return Refuse(http.StatusForbidden, CodeScope, "the token does not cover "+repo.Name)
		}
		if sc.Open != nil {
			q := *p
			q.Cred, q.Publish = auth.CredPublish, sc.Open
			p = &q
		}
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
