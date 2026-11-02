package access

import (
	"context"
	"net/http"
	"net/netip"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// Op is an operation on one repo.
type Op = auth.Op

const (
	Meta   = auth.OpMeta   // read the repo record: any repository permission
	Read   = auth.OpRead   // download
	Write  = auth.OpWrite  // store a file (format write, upload)
	Delete = auth.OpDelete // remove files
	Manage = auth.OpManage // change repo settings, grants, networks
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
	CodeScope          = "scope"
)

// Decision is the answer of the engine. A refusal has the HTTP status (401, 403, 404, 405 or
// 409), a code and a message for the caller.
type Decision struct {
	Allowed bool
	Status  int
	Code    string
	Message string
	// Perms are the caller's repository permissions on the repo (allowed or not).
	Perms perm.Set
}

// Refuse returns a refusal.
func Refuse(status int, code, msg string) Decision {
	return Decision{Status: status, Code: code, Message: msg}
}

// Networks are the allowed networks of the repos (the network module): a client address in the
// allowed networks of a repo downloads from it, with or without a credential. Every method reads
// memory only.
type Networks interface {
	// Allows reports whether a is in the allowed networks of repo id.
	Allows(repoID int64, a netip.Addr) bool
	// Reach returns the ids of the repos whose allowed networks hold a, in no order: one lookup
	// of an index, whatever the number of repos with networks.
	Reach(a netip.Addr) []int64
	// Has reports whether repo id has allowed networks.
	Has(repoID int64) bool
	// Describe returns the allowed networks of repo id for the access report.
	Describe(repoID int64) NetworkReport
}

// NetworkReport is the allowed networks of a repo in the access report: the IP lists by name and
// title, the address ranges with their notes, and Summary, the networks in words ("GitHub
// Actions and 10.0.0.0/8"; "" for none).
type NetworkReport struct {
	Lists   []NetworkList `json:"lists"`
	CIDRs   []NetworkCIDR `json:"cidrs"`
	Summary string        `json:"summary"`
}

// NetworkList is an IP list of a repo's allowed networks.
type NetworkList struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// NetworkCIDR is an address range of a repo's allowed networks.
type NetworkCIDR struct {
	CIDR string `json:"cidr"`
	Note string `json:"note"`
}

// Engine is the one policy engine of repo access: the route policies, the publish-session mint,
// the group read path and the access report all ask it.
type Engine struct {
	q        *dbq.Queries
	repos    *repos.Service
	auth     *auth.Service
	set      settings.Provider
	ops      *ops.Service
	networks Networks // nil without the network module; set before the server serves

	pipelines Pipelines      // the CI rules of the report; set before the server serves
	notes     []DownloadNote // the notes of the report's downloads; set before the server serves
}

// New returns the engine. os gives the message of a repo's operation.
func New(d *db.DB, rs *repos.Service, a *auth.Service, set settings.Provider, os *ops.Service) *Engine {
	return &Engine{q: dbq.New(d.R), repos: rs, auth: a, set: set, ops: os}
}

// SetNetworks sets the source of the allowed networks (the network module's Init). It runs
// before the server serves.
func (e *Engine) SetNetworks(n Networks) { e.networks = n }

// reaches reports whether the client address of the request of ctx is in the allowed networks
// of repo id.
func (e *Engine) reaches(ctx context.Context, repoID int64) bool {
	if e.networks == nil {
		return false
	}
	a := httpx.ClientAddr(ctx)
	return a.IsValid() && e.networks.Allows(repoID, a)
}

// NetworkRepos returns the ids of the repos whose allowed networks hold the client address of
// the request of ctx (nil for none): the repo lists, search and the registry catalog merge them
// as they merge the repos of grants.
func (e *Engine) NetworkRepos(ctx context.Context) []int64 {
	if e.networks == nil {
		return nil
	}
	a := httpx.ClientAddr(ctx)
	if !a.IsValid() {
		return nil
	}
	return e.networks.Reach(a)
}

// RepoPerms returns what p may do on repo: the grants of its credential, plus repo.download when
// the client address of the request of ctx is in the repo's allowed networks. A publish session
// holds, on its own repo, download and publish (a CI session) or the user's current download,
// publish and delete (a device or manual session); on another repo it holds what anonymous
// holds. Every other credential holds its grants (every permission with All repositories; a
// device holds the grants of its user), and anyone holds download on a public repo.
func (e *Engine) RepoPerms(ctx context.Context, p *auth.Principal, repo *repos.Repo) (perm.Set, error) {
	set, err := e.granted(ctx, p, repo)
	if err != nil || set.Has(perm.RepoDownload) || !e.reaches(ctx, repo.ID) {
		return set, err
	}
	return set.Union(perm.SetOf(perm.RepoDownload)), nil
}

// granted is RepoPerms without the allowed networks.
func (e *Engine) granted(ctx context.Context, p *auth.Principal, repo *repos.Repo) (perm.Set, error) {
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
// need (none: any permission, for Meta). In order: the scopes of a core bearer token, the
// permissions of p (RepoPerms, the allowed networks included), the repo kind (405 for a write to
// a proxy or a group), the permission (401 for anonymous, 401 for a write without an open
// publish session of the repo, else 403), then the operation of the repo (409).
func (e *Engine) Decide(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, op Op, need ...perm.ID) Decision {
	return e.decide(ctx, p, repo, op, need)
}

// Allows is Decide as a yes or no: a group skips a member that p may not read.
func (e *Engine) Allows(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, op Op, need ...perm.ID) (bool, error) {
	d := e.decide(ctx, p, repo, op, need)
	if d.Status == http.StatusInternalServerError {
		return false, errInternal
	}
	return d.Allowed, nil
}

// GroupMembers returns the members of group that p reads through r, in read order: a group read
// skips a member that p may not read. p read the group already. When the client address is in
// the group's allowed networks, p reads every member through the group: a group's networks reach
// the files it serves. A core bearer token reads each member with the rights of its credential
// (auth.Principal.InGroup), since its scopes name the group only. The grants of p's user are read
// once for all members (auth.Service.WithGrants).
func (e *Engine) GroupMembers(ctx context.Context, r *http.Request, p *auth.Principal, group *repos.Repo) ([]*repos.Repo, error) {
	members, err := e.repos.MemberRepos(ctx, group)
	if err != nil {
		return nil, err
	}
	if e.reaches(ctx, group.ID) {
		return members, nil
	}
	q := p.InGroup(group.ID)
	if ctx, err = e.auth.WithGrants(ctx, q); err != nil {
		return nil, err
	}
	out := make([]*repos.Repo, 0, len(members))
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

// Private reports whether a read of repo can serve a file that not everyone reads: repo is not
// public, or it is a group with a member that is not. A format read of such a repo answers with
// Cache-Control: private, so a shared cache (a CDN) does not keep it.
func (e *Engine) Private(ctx context.Context, repo *repos.Repo) (bool, error) {
	if !repo.AnonymousRead {
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
		if !m.AnonymousRead {
			return true, nil
		}
	}
	return false, nil
}

// Guarded reports, from memory, whether repo itself (not its members) has allowed networks: who
// reads it then depends on the client address, so the edge Worker leaves its reads to the
// server.
func (e *Engine) Guarded(repo *repos.Repo) bool {
	return e.networks != nil && e.networks.Has(repo.ID)
}

type internalError struct{}

func (internalError) Error() string { return "access: internal error" }

var errInternal error = internalError{}

func (e *Engine) decide(ctx context.Context, p *auth.Principal, repo *repos.Repo, op Op, need []perm.ID) Decision {
	if p != nil && p.Scopes != nil {
		sc := p.ScopeOf(repo.ID)
		check := op
		if check == Meta {
			check = Read
		}
		if sc == nil || !sc.Allows(check) {
			return e.scopeRefusal(sc, repo, op)
		}
		p = p.For(repo.ID)
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
	return Decision{Allowed: true, Perms: set}
}

// scopeRefusal is the answer to a core bearer token whose scope sc of repo (nil: none) lacks op.
// A token that reads the repo gets the answer of its credential: a write or delete on a proxy or
// a group is the read-only 405, and a write elsewhere needs a publish session (401). Any other
// case is the code CodeScope (401): the token does not hold the operation.
func (e *Engine) scopeRefusal(sc *auth.Scope, repo *repos.Repo, op Op) Decision {
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
