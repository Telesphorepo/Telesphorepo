package auth

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// grantsKey is the context key of the grants of one user (WithGrants).
type grantsKey struct{}

// grants are the granted repository permissions of a user by repo id.
type grants struct {
	user int64
	sets map[int64]perm.Set
}

// WithGrants returns ctx with the granted repository permissions of p's user, read in one query,
// so that RepoPerms on many repos (the members of a group, the list of repos) reads no row per
// repo. ctx comes back as it is when p needs no rows (anonymous, or all repositories) or holds
// them already.
func (s *Service) WithGrants(ctx context.Context, p *Principal) (context.Context, error) {
	if p == nil || p.UserID == 0 || perm.Current().AllRepos(p.Perms) {
		return ctx, nil
	}
	if g, ok := ctx.Value(grantsKey{}).(*grants); ok && g.user == p.UserID {
		return ctx, nil
	}
	sets, err := s.UserRepoPerms(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, grantsKey{}, &grants{user: p.UserID, sets: sets}), nil
}

// Granted returns the granted repository permissions of p's user by repo id that ctx holds
// (WithGrants), or nil when it holds none: for anonymous, for a holder of all repositories, and
// for a ctx that WithGrants did not make.
func Granted(ctx context.Context, p *Principal) map[int64]perm.Set {
	if p == nil || p.UserID == 0 {
		return nil
	}
	if g, ok := ctx.Value(grantsKey{}).(*grants); ok && g.user == p.UserID {
		return g.sets
	}
	return nil
}

// RepoPerms returns what p may do on repo: the repository permissions of p's grants, every one
// for a holder of a permission that gives all repositories (repositories.all), and
// repo.download for anyone on a repo with anonymous read. The grants come from ctx when it holds
// those of p's user (WithGrants).
func (s *Service) RepoPerms(ctx context.Context, p *Principal, repo *repos.Repo) (perm.Set, error) {
	var set perm.Set
	if repo.AnonymousRead {
		set = perm.SetOf(perm.RepoDownload)
	}
	if p == nil {
		return set, nil
	}
	g := perm.Current()
	if g.AllRepos(p.Perms) {
		return g.AllRepo(), nil
	}
	if p.UserID == 0 {
		return set, nil
	}
	if gr, ok := ctx.Value(grantsKey{}).(*grants); ok && gr.user == p.UserID {
		return set.Union(gr.sets[repo.ID]), nil
	}
	rows, err := s.rq.ListRepoUserPerms(ctx, dbq.ListRepoUserPermsParams{RepoID: repo.ID, UserID: p.UserID})
	if err != nil {
		return perm.Set{}, err
	}
	return set.Union(perm.SetOfStrings(rows).Of(perm.ScopeRepo)), nil
}

// DownloadableRepoIDs returns the ids of the repos where p holds repo.download (a grant, or
// anonymous read, served by the partial index repos_anonymous), or all=true for a holder of all
// repositories.
func (s *Service) DownloadableRepoIDs(ctx context.Context, p *Principal) ([]int64, bool, error) {
	if p != nil && perm.Current().AllRepos(p.Perms) {
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
	seen := make(map[int64]bool, len(anon))
	for _, id := range anon {
		seen[id] = true
	}
	for _, g := range rows {
		if perm.ID(g.Perm) == perm.RepoDownload && !seen[g.RepoID] {
			seen[g.RepoID] = true
			ids = append(ids, g.RepoID)
		}
	}
	return ids, false, nil
}

// ManagedRepoIDs returns the ids of the repos where p holds need, or all=true for a holder of all
// repositories.
func (s *Service) ManagedRepoIDs(ctx context.Context, p *Principal, need perm.ID) (map[int64]bool, bool, error) {
	if p != nil && perm.Current().AllRepos(p.Perms) {
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
// all repositories or anonymous read).
func (s *Service) UserRepoPerms(ctx context.Context, userID int64) (map[int64]perm.Set, error) {
	rows, err := s.rq.ListUserGrants(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := map[int64]perm.Set{}
	for _, g := range rows {
		out[g.RepoID] = out[g.RepoID].Union(perm.SetOf(perm.ID(g.Perm)).Of(perm.ScopeRepo))
	}
	for id, set := range out {
		if set.Empty() {
			delete(out, id)
		}
	}
	return out, nil
}

// needsFactor reports whether a person who holds the server permissions server needs a second
// factor for a UI session. repoManaging reports whether the person holds, on some repo, a
// repository permission that needs a second factor (managingRepoPerms).
func needsFactor(server perm.Set, repoManaging bool) bool {
	return repoManaging || perm.Current().SecondFactor(server)
}

// managingRepoPerms returns the repository permissions that need a second factor.
func managingRepoPerms() []string {
	ids := perm.Current().Managing(perm.ScopeRepo)
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

// userNeedsFactor reads whether user id needs a second factor for a UI session, in q.
func userNeedsFactor(ctx context.Context, q *dbq.Queries, id int64) (bool, error) {
	server, err := q.ListUserPerms(ctx, id)
	if err != nil {
		return false, err
	}
	repoManaging, err := q.HasRepoPermIn(ctx, dbq.HasRepoPermInParams{UserID: id, Perms: managingRepoPerms()})
	if err != nil {
		return false, err
	}
	return needsFactor(perm.SetOfStrings(server), repoManaging), nil
}

// Grant is one pair of a user and a repo whose permission set SetPermissions changed.
type Grant struct {
	Repo     *repos.Repo
	Username string
	Old, New perm.Set
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

// ErrNotHeld refuses a change of a permission that the caller does not hold.
var ErrNotHeld = errors.New("you can give or remove only the permissions that you hold")

// GrantGroup is one part of a permission change: every pair of Users and Repos gets the set Perms.
type GrantGroup struct {
	Users, Repos []string
	Perms        []string
}

// SetPermissions sets the repository permission set of every pair of users and repoNames to
// ids: SetGroups with one group.
func (s *Service) SetPermissions(ctx context.Context, by *Principal, users, repoNames []string, ids []string) ([]Grant, error) {
	return s.SetGroups(ctx, by, []GrantGroup{{Users: users, Repos: repoNames, Perms: ids}})
}

// SetGroups sets, for each group, the repository permission set of every pair of its users and
// repos to its perms, all groups in one transaction; no perms removes the pairs. It checks each
// set against the permission registry (an unknown ID or a broken need is an error) and reads
// every name first: an unknown name returns a *repos.MissingError and changes nothing. by is the
// caller: on each repo it may add or remove only the permissions it holds there (ErrNotHeld,
// and nothing changes). by nil is the server itself (the dev command, the Nexus moves), which
// holds every permission. A user who needed no second factor before and needs one after the
// whole change loses their UI sessions. Each changed pair records grant.set in the same
// transaction. A pair named by two groups gets the set of the last one. It returns the pairs
// whose set changed.
func (s *Service) SetGroups(ctx context.Context, by *Principal, groups []GrantGroup) ([]Grant, error) {
	g := perm.Current()
	sets := make([]perm.Set, len(groups))
	for k, gr := range groups {
		set, err := g.Parse(perm.ScopeRepo, gr.Perms)
		if err != nil {
			return nil, err
		}
		sets[k] = set
	}
	var changed []Grant
	var order []string           // the usernames, in their first order
	var userIDs map[string]int64 // by username
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		changed = changed[:0]
		order = order[:0]
		userIDs = map[string]int64{}
		q := s.wq.WithTx(tx)
		type part struct {
			uids []int64
			rs   []*repos.Repo
		}
		parts := make([]part, len(groups))
		held := map[int64]perm.Set{} // by repo ID
		before := map[int64]bool{}   // by user ID: needed a second factor before the change
		for k, gr := range groups {
			uids, rs, err := s.pairs(ctx, q, gr.Users, gr.Repos)
			if err != nil {
				return err
			}
			parts[k] = part{uids, rs}
			for _, r := range rs {
				if _, ok := held[r.ID]; ok {
					continue
				}
				if by == nil {
					held[r.ID] = g.AllRepo()
				} else if held[r.ID], err = s.RepoPerms(ctx, by, r); err != nil {
					return err
				}
			}
			for i, uid := range uids {
				if _, ok := userIDs[gr.Users[i]]; ok {
					continue
				}
				userIDs[gr.Users[i]] = uid
				order = append(order, gr.Users[i])
				if before[uid], err = userNeedsFactor(ctx, q, uid); err != nil {
					return err
				}
			}
		}
		for k, gr := range groups {
			perms := sets[k]
			for i, uid := range parts[k].uids {
				for _, r := range parts[k].rs {
					rows, err := q.ListRepoUserPerms(ctx, dbq.ListRepoUserPermsParams{RepoID: r.ID, UserID: uid})
					if err != nil {
						return err
					}
					old := perm.SetOfStrings(rows)
					if old == perms {
						continue
					}
					diff := old.Minus(perms).Union(perms.Minus(old))
					if !held[r.ID].Contains(diff) {
						return ErrNotHeld
					}
					if _, err := q.DeleteRepoUserPerms(ctx, dbq.DeleteRepoUserPermsParams{RepoID: r.ID, UserID: uid}); err != nil {
						return err
					}
					for _, n := range perms.Strings() {
						if err := q.AddRepoPerm(ctx, dbq.AddRepoPermParams{RepoID: r.ID, UserID: uid, Perm: n}); err != nil {
							if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
								return &repos.MissingError{Kind: "repository", Name: r.Name}
							}
							return err
						}
					}
					if err := s.audit.RecordTx(ctx, tx, audit.Event{Action: audit.GrantSet, RepoID: r.ID, Repo: r.Name,
						Target: gr.Users[i], Detail: map[string]any{"old": old.Strings(), "new": perms.Strings()}}); err != nil {
						return err
					}
					changed = append(changed, Grant{Repo: r, Username: gr.Users[i], Old: old, New: perms})
				}
			}
		}
		for _, name := range order {
			uid := userIDs[name]
			after, err := userNeedsFactor(ctx, q, uid)
			if err != nil {
				return err
			}
			if !before[uid] && after {
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
	if len(changed) > 0 {
		names := map[string]bool{}
		for _, g := range changed {
			names[g.Username] = true
		}
		c := Change{}
		for _, name := range order {
			if names[name] {
				c.Users = append(c.Users, userIDs[name])
			}
		}
		s.changed(ctx, c)
	}
	return changed, nil
}

// ListPermissions returns the permission sets on repo, by username.
func (s *Service) ListPermissions(ctx context.Context, repoID int64) ([]Permission, error) {
	rows, err := s.rq.ListPermissions(ctx, repoID)
	if err != nil {
		return nil, err
	}
	by := map[string]perm.Set{}
	for _, r := range rows {
		by[r.Username] = by[r.Username].Union(perm.SetOf(perm.ID(r.Perm)))
	}
	out := make([]Permission, 0, len(by))
	for u, set := range by {
		if !set.Empty() {
			out = append(out, Permission{Username: u, Perms: set.Strings()})
		}
	}
	slices.SortFunc(out, func(a, b Permission) int { return strings.Compare(a.Username, b.Username) })
	return out, nil
}
