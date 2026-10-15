package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// RepoPerms returns what p may do on repo: the repository permissions of p's grants, every one
// for a holder of PermRepos, and RepoDownload for anyone on a repo with anonymous read.
func (s *Service) RepoPerms(ctx context.Context, p *Principal, repo *repos.Repo) (RepoPerm, error) {
	var set RepoPerm
	if repo.AnonymousRead {
		set = RepoDownload
	}
	if p == nil {
		return set, nil
	}
	if p.Perms.Has(PermRepos) {
		return AllRepoPerms, nil
	}
	if p.UserID == 0 {
		return set, nil
	}
	rows, err := s.rq.ListRepoUserPerms(ctx, dbq.ListRepoUserPermsParams{RepoID: repo.ID, UserID: p.UserID})
	if err != nil {
		return 0, err
	}
	for _, n := range rows {
		set |= repoPermOf(n)
	}
	return set, nil
}

// gatePerms is what p may do on repo through a format route (spec §7.6). Only a publish session
// publishes, and only to its own repo: an OIDC session publishes there outright, a device or
// manual session as far as its user's current permissions allow. A session downloads from no
// other repo beyond anonymous read. Every other credential downloads at most.
func (s *Service) gatePerms(ctx context.Context, p *Principal, repo *repos.Repo) (RepoPerm, error) {
	if p != nil && p.Publish != nil {
		if p.Publish.RepoID != repo.ID {
			return s.RepoPerms(ctx, nil, repo)
		}
		if p.Publish.Via == "oidc" {
			return RepoDownload | RepoPublish, nil
		}
		set, err := s.RepoPerms(ctx, p, repo)
		return set & (RepoDownload | RepoPublish | RepoDelete), err
	}
	set, err := s.RepoPerms(ctx, p, repo)
	return set & RepoDownload, err
}

// publishRequired answers a publish without an open publish session for the repo.
func (s *Service) publishRequired(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimSuffix(s.cfg().BaseURL, "/")
	unauthorized(w, r, "publishing needs a publish session: "+base+"/#/settings/tokens")
}

// RepoGate authenticates a format-route request and checks it may do need (RepoDownload or
// RepoPublish) on repo name. A publish needs an open publish session for that repo; anything
// else gets 401 with the Basic challenge (none on the JSON API or for the web UI). A user who
// lost the publish permission gets 403, a locked repo 409.
// The repo's IP rules apply to every caller, holders of PermRepos included: the read rules to a
// download, the publish rules to a publish. A refused address gets 403 and an ip.denied audit
// event. allowSession is true on the JSON API. A format route (allowSession false) also takes
// the UI session cookie for a GET or HEAD download, so a signed-in browser downloads without a
// Basic prompt.
func (s *Service) RepoGate(w http.ResponseWriter, r *http.Request, name, format string, need RepoPerm, allowSession bool) (*repos.Repo, *Principal, bool) {
	p, ok := s.identify(w, r, allowSession, allowSession || (need == RepoDownload && safeMethod(r.Method)))
	if !ok {
		return nil, nil, false
	}
	repo, p, ok := s.gate(w, r, p, name, format, need)
	if !ok || !s.ipAllowed(w, r, p, repo, need) {
		return nil, nil, false
	}
	return repo, p, true
}

// RepoMeta is RepoGate for a read of the repo record itself (GET /api/repos/{name}): it does not
// apply the IP rules, so a person with RepoSettings can still open the repo to change them. A
// person who holds a repository permission but not RepoDownload reads the record too.
func (s *Service) RepoMeta(w http.ResponseWriter, r *http.Request, name string) (*repos.Repo, *Principal, bool) {
	p, ok := s.Identify(w, r, true)
	if !ok {
		return nil, nil, false
	}
	if p != nil && p.Session && p.Publish == nil {
		repo, err := s.repos.ByName(r.Context(), name)
		if err == nil {
			set, err := s.RepoPerms(r.Context(), p, repo)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "internal error")
				return nil, nil, false
			}
			if set != 0 {
				return repo, p, true
			}
		}
	}
	return s.gate(w, r, p, name, "", RepoDownload)
}

// ipAllowed checks the repo's IP rules for need (a download or a publish) and answers 403 when
// they refuse the client address.
func (s *Service) ipAllowed(w http.ResponseWriter, r *http.Request, p *Principal, repo *repos.Repo, need RepoPerm) bool {
	if s.rules == nil {
		return true
	}
	scope := netrules.ScopeRead
	if need != RepoDownload {
		scope = netrules.ScopePublish
	}
	if d := s.rules.CheckRequest(r, repo, scope, ActorOf(p)); d != nil {
		s.rules.Refuse(w, r, d)
		return false
	}
	return true
}

// DeleteGate authorizes a delete of files through the JSON API (spec §13): the caller holds
// RepoDelete on the repo, with a UI session or with a device or manual publish session for the
// repo. A UI session that holds RepoDelete exists only after the second factor (outside dev
// mode), and a publish session needed one or a device proof. The repo's publish rules apply. A
// locked repo answers 409.
func (s *Service) DeleteGate(w http.ResponseWriter, r *http.Request, name string) (*repos.Repo, *Principal, bool) {
	p, ok := s.Identify(w, r, true)
	if !ok {
		return nil, nil, false
	}
	var repo *repos.Repo
	if p != nil && p.Publish != nil {
		repo, p, ok = s.gate(w, r, p, name, "", RepoDelete)
		if !ok {
			return nil, nil, false
		}
	} else {
		if p == nil || !p.Session {
			SessionRequired(w)
			return nil, nil, false
		}
		var err error
		repo, err = s.repos.ByName(r.Context(), name)
		if errors.Is(err, repos.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "repo not found")
			return nil, nil, false
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return nil, nil, false
		}
		set, err := s.RepoPerms(r.Context(), p, repo)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return nil, nil, false
		}
		if !set.Has(RepoDelete) {
			httpx.Error(w, http.StatusForbidden, "forbidden")
			return nil, nil, false
		}
		if repo.LockedBy != "" {
			httpx.Error(w, http.StatusConflict, repo.LockedBy)
			return nil, nil, false
		}
	}
	if !s.ipAllowed(w, r, p, repo, RepoDelete) {
		return nil, nil, false
	}
	return repo, p, true
}

// gate checks that the identified caller p may do need on repo name (see RepoGate).
func (s *Service) gate(w http.ResponseWriter, r *http.Request, p *Principal, name, format string, need RepoPerm) (*repos.Repo, *Principal, bool) {
	var repo *repos.Repo
	writes := need != RepoDownload
	denied := func() (*repos.Repo, *Principal, bool) {
		switch {
		case writes && (p == nil || p.Publish == nil || (repo != nil && p.Publish.RepoID != repo.ID)):
			s.publishRequired(w, r)
		case p == nil:
			unauthorized(w, r, "authentication required")
		default:
			httpx.Error(w, http.StatusForbidden, "forbidden")
		}
		return nil, nil, false
	}
	repo, err := s.repos.ByName(r.Context(), name)
	if errors.Is(err, repos.ErrNotFound) {
		if p == nil {
			return denied()
		}
		httpx.Error(w, http.StatusNotFound, "repo not found")
		return nil, nil, false
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return nil, nil, false
	}
	set, err := s.gatePerms(r.Context(), p, repo)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return nil, nil, false
	}
	if format != "" && repo.Format != format {
		if set == 0 && p == nil {
			return denied() // don't reveal that a repo of another format exists
		}
		httpx.Error(w, http.StatusNotFound, "repo not found")
		return nil, nil, false
	}
	// A proxy or a group takes no publishes. Say so to a caller who can download from it; anyone
	// else gets the usual refusal, so the answer does not tell that the repo exists.
	if writes && repo.Kind != repos.KindHosted && set.Has(RepoDownload) {
		w.Header().Set("Allow", "GET, HEAD")
		httpx.Error(w, http.StatusMethodNotAllowed, ReadOnly(repo))
		return nil, nil, false
	}
	if !set.Has(need) {
		return denied()
	}
	// A locked repo takes no publishes through HTTP; its text names the owner, e.g. the Nexus
	// source that the repo mirrors.
	if writes && repo.LockedBy != "" {
		httpx.Error(w, http.StatusConflict, repo.LockedBy)
		return nil, nil, false
	}
	return repo, p, true
}

// ReadOnly is the answer to a publish to a proxy or a group repo.
func ReadOnly(repo *repos.Repo) string {
	if repo.Kind == repos.KindGroup {
		return "A group is read-only. Publish to one of its repositories."
	}
	return "A proxy is read-only."
}

// ReadsThrough reports whether the caller p of r may download from repo as a member of a group:
// p's access to repo (as on its own route) and repo's read rules for the client address both
// count. It writes no answer and no audit event; the group skips a member that p cannot read.
func (s *Service) ReadsThrough(ctx context.Context, r *http.Request, p *Principal, repo *repos.Repo) (bool, error) {
	set, err := s.gatePerms(ctx, p, repo)
	if err != nil || !set.Has(RepoDownload) {
		return false, err
	}
	if s.rules != nil && s.rules.CheckRequest(r, repo, netrules.ScopeRead, ActorOf(p)) != nil {
		return false, nil
	}
	return true, nil
}

// ReadableRepoIDs returns the ids of the repos where p holds a permission or that are anonymous,
// or all=true for a holder of PermRepos. It merges the user's permission rows with the anonymous
// repos (served by the partial index repos_anonymous). Callers that list files check
// RepoDownload per repo.
func (s *Service) ReadableRepoIDs(ctx context.Context, p *Principal) ([]int64, bool, error) {
	if p != nil && p.Perms.Has(PermRepos) {
		return nil, true, nil
	}
	anon, err := s.rq.ListAnonymousRepoIDs(ctx)
	if err != nil {
		return nil, false, err
	}
	if p == nil || p.UserID == 0 {
		return anon, false, nil
	}
	permitted, err := s.rq.ListPermittedRepoIDs(ctx, p.UserID)
	if err != nil {
		return nil, false, err
	}
	seen := make(map[int64]struct{}, len(anon)+len(permitted))
	ids := make([]int64, 0, len(anon)+len(permitted))
	for _, id := range append(permitted, anon...) {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids, false, nil
}

// DownloadableRepoIDs is ReadableRepoIDs limited to the repos where p holds RepoDownload (a
// grant of RepoDownload, or anonymous read).
func (s *Service) DownloadableRepoIDs(ctx context.Context, p *Principal) ([]int64, bool, error) {
	if p != nil && p.Perms.Has(PermRepos) {
		return nil, true, nil
	}
	anon, err := s.rq.ListAnonymousRepoIDs(ctx)
	if err != nil {
		return nil, false, err
	}
	if p == nil || p.UserID == 0 {
		return anon, false, nil
	}
	rows, err := s.rq.ListUserGrants(ctx, p.UserID)
	if err != nil {
		return nil, false, err
	}
	ids := anon
	for _, g := range rows {
		if g.Perm == "download" && !slices.Contains(ids, g.RepoID) {
			ids = append(ids, g.RepoID)
		}
	}
	return ids, false, nil
}

// ManagedRepoIDs returns the ids of the repos where p holds every permission of need, or
// all=true for a holder of PermRepos.
func (s *Service) ManagedRepoIDs(ctx context.Context, p *Principal, need RepoPerm) (map[int64]bool, bool, error) {
	if p != nil && p.Perms.Has(PermRepos) {
		return nil, true, nil
	}
	out := map[int64]bool{}
	if p == nil || p.UserID == 0 {
		return out, false, nil
	}
	sets, err := s.UserRepoPerms(ctx, p.UserID)
	if err != nil {
		return nil, false, err
	}
	for id, set := range sets {
		if set.Has(need) {
			out[id] = true
		}
	}
	return out, false, nil
}

// UserRepoPerms returns the granted repository permissions of a user by repo id (not counting
// PermRepos or anonymous read).
func (s *Service) UserRepoPerms(ctx context.Context, userID int64) (map[int64]RepoPerm, error) {
	rows, err := s.rq.ListUserGrants(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := map[int64]RepoPerm{}
	for _, g := range rows {
		out[g.RepoID] |= repoPermOf(g.Perm)
	}
	return out, nil
}

// Grant is one pair of a user and a repo whose permission set SetPermissions changed.
type Grant struct {
	Repo     *repos.Repo
	Username string
	Old, New RepoPerm
}

// pairs reads every user and repo of a list change of permissions. An unknown name returns a
// *repos.MissingError. The repos come from the read pool; a repo deleted before the write
// transaction ends fails its foreign key, and the transaction changes nothing.
func (s *Service) pairs(ctx context.Context, q *dbq.Queries, users, repoNames []string) ([]int64, []*repos.Repo, error) {
	rs := make([]*repos.Repo, 0, len(repoNames))
	for _, name := range repoNames {
		r, err := s.repos.ByName(ctx, name)
		if errors.Is(err, repos.ErrNotFound) {
			return nil, nil, &repos.MissingError{Kind: "repository", Name: name}
		}
		if err != nil {
			return nil, nil, err
		}
		rs = append(rs, r)
	}
	ids := make([]int64, 0, len(users))
	for _, name := range users {
		row, err := q.GetUserIDKind(ctx, name)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, &repos.MissingError{Kind: "user", Name: name}
		}
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, row.ID)
	}
	return ids, rs, nil
}

// ErrPermDeps refuses a repository permission set that breaks the dependency rules.
var ErrPermDeps = errors.New("publish needs download, and CI pipelines needs publish")

// ErrNotHeld refuses a change of a permission that the caller does not hold.
var ErrNotHeld = errors.New("you can give or remove only the permissions that you hold")

// SetPermissions sets the repository permission set of every pair of users and repoNames to
// perms, in one transaction; perms 0 removes the pair. It reads every name first: an unknown
// name returns a *repos.MissingError and changes nothing. by is the caller: on each repo it may
// add or remove only the permissions it holds there (ErrNotHeld). by nil is the server itself
// (the dev command), which holds every permission. A user who needed no second
// factor before and needs one now loses their UI sessions. It returns the pairs whose set
// changed.
func (s *Service) SetPermissions(ctx context.Context, by *Principal, users, repoNames []string, perms RepoPerm) ([]Grant, error) {
	if !perms.Valid() {
		return nil, ErrPermDeps
	}
	var changed []Grant
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		changed = changed[:0]
		q := s.wq.WithTx(tx)
		ids, rs, err := s.pairs(ctx, q, users, repoNames)
		if err != nil {
			return err
		}
		held := make([]RepoPerm, len(rs))
		for j, r := range rs {
			if by == nil {
				held[j] = AllRepoPerms
			} else if held[j], err = s.RepoPerms(ctx, by, r); err != nil {
				return err
			}
		}
		for i, uid := range ids {
			before, err := q.UserElevated(ctx, uid)
			if err != nil {
				return err
			}
			for j, r := range rs {
				rows, err := q.ListRepoUserPerms(ctx, dbq.ListRepoUserPermsParams{RepoID: r.ID, UserID: uid})
				if err != nil {
					return err
				}
				var old RepoPerm
				for _, n := range rows {
					old |= repoPermOf(n)
				}
				if old == perms {
					continue
				}
				if (old^perms)&^held[j] != 0 {
					return ErrNotHeld
				}
				if _, err := q.DeleteRepoUserPerms(ctx, dbq.DeleteRepoUserPermsParams{RepoID: r.ID, UserID: uid}); err != nil {
					return err
				}
				for _, n := range perms.Names() {
					if err := q.AddRepoPerm(ctx, dbq.AddRepoPermParams{RepoID: r.ID, UserID: uid, Perm: n}); err != nil {
						if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
							return &repos.MissingError{Kind: "repository", Name: r.Name}
						}
						return err
					}
				}
				changed = append(changed, Grant{Repo: r, Username: users[i], Old: old, New: perms})
			}
			if !before.Bool && perms.Elevated() {
				if err := q.DeleteUserSessions(ctx, uid); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// ListPermissions returns the permission sets on repo, by username.
func (s *Service) ListPermissions(ctx context.Context, repoID int64) ([]Permission, error) {
	rows, err := s.rq.ListPermissions(ctx, repoID)
	if err != nil {
		return nil, err
	}
	by := map[string]RepoPerm{}
	for _, r := range rows {
		by[r.Username] |= repoPermOf(r.Perm)
	}
	out := make([]Permission, 0, len(by))
	for u, set := range by {
		out = append(out, Permission{Username: u, Perms: set.Names()})
	}
	slices.SortFunc(out, func(a, b Permission) int { return strings.Compare(a.Username, b.Username) })
	return out, nil
}
