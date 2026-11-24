package auth

import (
	"context"
	"database/sql"
	"encoding/json"
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

// usersNeedFactor is userNeedsFactor for the users of ids, by id: two queries whatever their
// number.
func usersNeedFactor(ctx context.Context, q *dbq.Queries, ids []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.ListUsersPerms(ctx, ids)
	if err != nil {
		return nil, err
	}
	server := map[int64][]string{}
	for _, r := range rows {
		server[r.UserID] = append(server[r.UserID], r.Perm)
	}
	users, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	perms, err := json.Marshal(managingRepoPerms())
	if err != nil {
		return nil, err
	}
	managing, err := q.ListUsersWithRepoPermIn(ctx, dbq.ListUsersWithRepoPermInParams{Users: string(users), Perms: string(perms)})
	if err != nil {
		return nil, err
	}
	repoManaging := make(map[int64]bool, len(managing))
	for _, id := range managing {
		repoManaging[id] = true
	}
	for _, id := range ids {
		out[id] = needsFactor(perm.SetOfStrings(server[id]), repoManaging[id])
	}
	return out, nil
}

// Grant is one pair of a user and a repo whose permission set SetPermissions changed.
type Grant struct {
	Repo     *repos.Repo
	Username string
	Old, New perm.Set
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

// pair is a repo and a user of a permission change.
type pair struct{ repo, user int64 }

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
//
// A change costs a fixed number of statements whatever its number of pairs: the repos are read
// in one lookup of the list (from the read pool; a repo deleted before the write transaction ends
// fails its foreign key, and the transaction changes nothing), the users, the second-factor needs,
// the old sets of the pairs, the deletes, the inserts and the events each in one statement.
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
	var repoNames, userNames []string // every name once, in its first order
	seen := map[string]bool{}
	for _, gr := range groups {
		for _, n := range gr.Repos {
			if !seen["r"+n] {
				seen["r"+n] = true
				repoNames = append(repoNames, n)
			}
		}
		for _, n := range gr.Users {
			if !seen["u"+n] {
				seen["u"+n] = true
				userNames = append(userNames, n)
			}
		}
	}
	reposBy, err := s.repos.ByNames(ctx, repoNames)
	if err != nil {
		return nil, err
	}
	// The caller's permissions on the repos come from its grants, read once.
	byCtx, err := s.WithGrants(ctx, by)
	if err != nil {
		return nil, err
	}
	var changed []Grant
	var order []string           // the usernames, in their first order
	var userIDs map[string]int64 // by username
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		changed = changed[:0]
		order = order[:0]
		userIDs = map[string]int64{}
		q := s.wq.WithTx(tx)
		named, err := q.ListUsersNamed(ctx, userNames)
		if err != nil {
			return err
		}
		ids := make(map[string]int64, len(named))
		for _, u := range named {
			ids[u.Username] = u.ID
		}
		type part struct {
			uids []int64
			rs   []*repos.Repo
		}
		parts := make([]part, len(groups))
		held := map[int64]perm.Set{} // by repo ID
		for k, gr := range groups {
			// Each group's repos, then its users, are checked in their order, so the first unknown
			// name is the one reported.
			rs := make([]*repos.Repo, 0, len(gr.Repos))
			for _, name := range gr.Repos {
				r := reposBy[name]
				if r == nil {
					return &repos.MissingError{Kind: "repository", Name: name}
				}
				rs = append(rs, r)
			}
			uids := make([]int64, 0, len(gr.Users))
			for _, name := range gr.Users {
				id, ok := ids[name]
				if !ok {
					return &repos.MissingError{Kind: "user", Name: name}
				}
				uids = append(uids, id)
			}
			parts[k] = part{uids, rs}
			for _, r := range rs {
				if _, ok := held[r.ID]; ok {
					continue
				}
				if by == nil {
					held[r.ID] = g.AllRepo()
				} else if held[r.ID], err = s.RepoPerms(byCtx, by, r); err != nil {
					return err
				}
			}
			for i, uid := range uids {
				if _, ok := userIDs[gr.Users[i]]; ok {
					continue
				}
				userIDs[gr.Users[i]] = uid
				order = append(order, gr.Users[i])
			}
		}
		uids := make([]int64, len(order))
		for i, name := range order {
			uids[i] = userIDs[name]
		}
		before, err := usersNeedFactor(ctx, q, uids)
		if err != nil {
			return err
		}
		var asked [][2]int64
		for _, pt := range parts {
			for _, uid := range pt.uids {
				for _, r := range pt.rs {
					asked = append(asked, [2]int64{r.ID, uid})
				}
			}
		}
		cur, err := pairSets(ctx, q, asked)
		if err != nil {
			return err
		}
		var touched []pair // the pairs whose set changes, in their first order
		isTouched := map[pair]bool{}
		var events []audit.Event
		for k, gr := range groups {
			perms := sets[k]
			for i, uid := range parts[k].uids {
				for _, r := range parts[k].rs {
					key := pair{r.ID, uid}
					old := cur[key]
					if old == perms {
						continue
					}
					diff := old.Minus(perms).Union(perms.Minus(old))
					if !held[r.ID].Contains(diff) {
						return ErrNotHeld
					}
					if !isTouched[key] {
						isTouched[key] = true
						touched = append(touched, key)
					}
					cur[key] = perms // a later group that names the pair changes it from here
					events = append(events, audit.Event{Action: audit.GrantSet, RepoID: r.ID, Repo: r.Name,
						Target: gr.Users[i], Detail: map[string]any{"old": old.Strings(), "new": perms.Strings()}})
					changed = append(changed, Grant{Repo: r, Username: gr.Users[i], Old: old, New: perms})
				}
			}
		}
		if len(touched) > 0 {
			if err := s.writePairs(ctx, tx, q, touched, cur, events); err != nil {
				return err
			}
		}
		after, err := usersNeedFactor(ctx, q, uids)
		if err != nil {
			return err
		}
		var ended []int64
		for _, uid := range uids {
			if !before[uid] && after[uid] {
				ended = append(ended, uid)
			}
		}
		if len(ended) > 0 {
			return q.DeleteUsersSessions(ctx, ended)
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

// pairSets reads the repository permission set of each pair of pairs ([repo, user]) in q: one
// query, a seek per pair. A pair without a grant holds the empty set.
func pairSets(ctx context.Context, q *dbq.Queries, pairs [][2]int64) (map[pair]perm.Set, error) {
	out := make(map[pair]perm.Set, len(pairs))
	if len(pairs) == 0 {
		return out, nil
	}
	js, err := json.Marshal(pairs)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListPairPerms(ctx, string(js))
	if err != nil {
		return nil, err
	}
	strs := map[pair][]string{}
	for _, r := range rows {
		k := pair{r.RepoID, r.UserID}
		strs[k] = append(strs[k], r.Perm)
	}
	for k, list := range strs {
		out[k] = perm.SetOfStrings(list)
	}
	return out, nil
}

// writePairs gives each pair of touched its set in sets, in tx, and records events: one delete,
// one insert of the grants and one of the events. A repo deleted since it was read fails its
// foreign key: the answer names it.
func (s *Service) writePairs(ctx context.Context, tx *sql.Tx, q *dbq.Queries, touched []pair, sets map[pair]perm.Set, events []audit.Event) error {
	drop := make([][2]int64, len(touched))
	var add [][3]any
	for i, k := range touched {
		drop[i] = [2]int64{k.repo, k.user}
		for _, n := range sets[k].Strings() {
			add = append(add, [3]any{k.repo, k.user, n})
		}
	}
	js, err := json.Marshal(drop)
	if err != nil {
		return err
	}
	if err := q.DeletePairPerms(ctx, string(js)); err != nil {
		return err
	}
	if len(add) > 0 {
		if js, err = json.Marshal(add); err != nil {
			return err
		}
		err = q.AddPairPerms(ctx, string(js))
	}
	if err == nil {
		err = s.audit.RecordAllTx(ctx, tx, events)
	}
	if err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		return goneRepo(ctx, q, touched, events, err)
	}
	return err
}

// goneRepo returns the *repos.MissingError of the first repo of touched that is gone, else err.
func goneRepo(ctx context.Context, q *dbq.Queries, touched []pair, events []audit.Event, err error) error {
	ids := make([]int64, len(touched))
	for i, k := range touched {
		ids[i] = k.repo
	}
	rows, qerr := q.ListReposByIDs(ctx, ids)
	if qerr != nil {
		return err
	}
	here := make(map[int64]bool, len(rows))
	for _, r := range rows {
		here[r.ID] = true
	}
	for _, e := range events {
		if !here[e.RepoID] {
			return &repos.MissingError{Kind: "repository", Name: e.Repo}
		}
	}
	return err
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
