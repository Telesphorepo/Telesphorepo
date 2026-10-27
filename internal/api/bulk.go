package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/route"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Write routes that change a set take a list (bulk actions). Each route checks every
// item before it changes any, and records one audit event per changed item.

// maxBulk is the most items one list may hold.
const maxBulk = 1000

// listOf checks that the list field of a body holds 1 to maxBulk items and returns them
// without repeats, in their first order. On failure it answers 400 and returns ok=false.
func listOf(w http.ResponseWriter, r *http.Request, field string, xs []string) ([]string, bool) {
	if len(xs) == 0 || len(xs) > maxBulk {
		httpx.Error(w, r, http.StatusBadRequest, field+" must hold 1 to "+strconv.Itoa(maxBulk)+" items")
		return nil, false
	}
	out := make([]string, 0, len(xs))
	seen := make(map[string]bool, len(xs))
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out, true
}

// canEach checks with the access engine that p may manage every repo of names with need (or one
// of them): 404 for an unknown name, else the refusal of the engine.
func (a *API) canEach(w http.ResponseWriter, r *http.Request, p *auth.Principal, names []string, need perm.ID) bool {
	for _, name := range names {
		repo, err := a.repos.ByName(r.Context(), name)
		if errors.Is(err, repos.ErrNotFound) {
			err = &repos.MissingError{Kind: "repository", Name: name}
		}
		if err != nil {
			route.Fail(w, r, err)
			return false
		}
		if d := a.access.Decide(r.Context(), r, p, repo, access.Manage, need); !d.Allowed {
			httpx.Error(w, r, d.Status, d.Message)
			return false
		}
	}
	return true
}

// setReposPublic makes the repos of names public or private (settings on each) in one repo update:
// {names, anonymous_read} -> 200 {changed: n}. An unknown name answers 404 and changes nothing.
func (a *API) setReposPublic(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	var in struct {
		Names         []string `json:"names"`
		AnonymousRead *bool    `json:"anonymous_read"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	names, ok := listOf(w, r, "names", in.Names)
	if !ok {
		return
	}
	if in.AnonymousRead == nil {
		httpx.Error(w, r, http.StatusBadRequest, "give anonymous_read")
		return
	}
	if !a.canEach(w, r, p, names, perm.RepoSettings) {
		return
	}
	_, changed, err := a.repos.UpdateAll(r.Context(), names, repos.Patch{AnonymousRead: in.AnonymousRead})
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"changed": changed})
}

// setUsersDisabled disables or enables the users of usernames (people): {usernames, disabled} ->
// 200 {changed: n}. An unknown name answers 404 and changes nothing. The caller cannot disable
// their own account, and changes only people whose permissions they all hold (403). A disabled
// user's sessions end. The last enabled person with people.manage stays enabled (409). The auth
// service records user.update for each changed person.
func (a *API) setUsersDisabled(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	var in struct {
		Usernames []string `json:"usernames"`
		Disabled  *bool    `json:"disabled"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	names, ok := listOf(w, r, "usernames", in.Usernames)
	if !ok {
		return
	}
	if in.Disabled == nil {
		httpx.Error(w, r, http.StatusBadRequest, "give disabled")
		return
	}
	if *in.Disabled && slices.Contains(names, p.Username) {
		httpx.Error(w, r, http.StatusBadRequest, "You cannot disable your own account.")
		return
	}
	for _, name := range names {
		u, err := a.auth.GetUser(r.Context(), name)
		if errors.Is(err, auth.ErrNotFound) {
			err = &repos.MissingError{Kind: "user", Name: name}
		}
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		if ok, err := a.auth.Covers(r.Context(), p, u); err != nil || !ok {
			if err != nil {
				route.Fail(w, r, err)
			} else {
				httpx.Error(w, r, http.StatusForbidden, "you can change only people whose permissions you all hold: "+name)
			}
			return
		}
	}
	changed, err := a.auth.SetDisabled(r.Context(), names, *in.Disabled)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"changed": len(changed)})
}

// pairsIn reads the users and repos lists of a permissions body. Both hold 1 to maxBulk items,
// and together at most maxBulk pairs.
func pairsIn(w http.ResponseWriter, r *http.Request, users, repoNames []string) ([]string, []string, bool) {
	us, ok := listOf(w, r, "users", users)
	if !ok {
		return nil, nil, false
	}
	rs, ok := listOf(w, r, "repos", repoNames)
	if !ok {
		return nil, nil, false
	}
	if len(us)*len(rs) > maxBulk {
		httpx.Error(w, r, http.StatusBadRequest, "give at most "+strconv.Itoa(maxBulk)+" pairs of user and repository")
		return nil, nil, false
	}
	return us, rs, true
}

// grantGroup is one part of a permissions body: every pair of Users and Repos gets Perms.
type grantGroup struct {
	Users []string `json:"users"`
	Repos []string `json:"repos"`
	Perms []string `json:"perms"`
}

// setPermissions sets the repository permission set of every pair of users and repos:
// {users, repos, perms} -> 200 {changed: n}, or several sets at once:
// {groups: [{users, repos, perms}, ...]}, applied in one transaction. perms [] removes the pairs.
// Each group holds 1 to maxBulk users and repos, the groups together at most maxBulk pairs, and
// no pair is in two groups (400). The caller holds repo.access on every repo (403) and adds or
// removes only permissions they hold there (403). An unknown permission ID or a permission
// without one it needs answers 400. An unknown user or repo answers 404. Any refusal changes
// nothing. The auth service records one grant.set event per pair whose set changed, with the old
// and the new set.
func (a *API) setPermissions(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	var in struct {
		grantGroup
		Groups []grantGroup `json:"groups"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	groups := in.Groups
	if len(groups) == 0 {
		groups = []grantGroup{in.grantGroup}
	} else if in.Users != nil || in.Repos != nil || in.Perms != nil {
		httpx.Error(w, r, http.StatusBadRequest, "give groups or users, repos and perms, not both")
		return
	}
	if len(groups) > maxBulk {
		httpx.Error(w, r, http.StatusBadRequest, "give at most "+strconv.Itoa(maxBulk)+" groups")
		return
	}
	parts := make([]auth.GrantGroup, 0, len(groups))
	var names []string // every repo, once
	seenRepo := map[string]bool{}
	seenPair := map[[2]string]bool{}
	for _, g := range groups {
		users, rs, ok := pairsIn(w, r, g.Users, g.Repos)
		if !ok {
			return
		}
		if len(seenPair)+len(users)*len(rs) > maxBulk {
			httpx.Error(w, r, http.StatusBadRequest, "give at most "+strconv.Itoa(maxBulk)+" pairs of user and repository")
			return
		}
		for _, u := range users {
			for _, n := range rs {
				if seenPair[[2]string{u, n}] {
					httpx.Error(w, r, http.StatusBadRequest, "give each pair of user and repository in one group: "+u+" on "+n)
					return
				}
				seenPair[[2]string{u, n}] = true
			}
		}
		for _, n := range rs {
			if !seenRepo[n] {
				seenRepo[n] = true
				names = append(names, n)
			}
		}
		if _, err := perm.Current().Parse(perm.ScopeRepo, g.Perms); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, err.Error())
			return
		}
		parts = append(parts, auth.GrantGroup{Users: users, Repos: rs, Perms: g.Perms})
	}
	if !a.canEach(w, r, p, names, perm.RepoAccess) {
		return
	}
	changed, err := a.auth.SetGroups(r.Context(), p, parts)
	if errors.Is(err, auth.ErrNotHeld) {
		httpx.Error(w, r, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"changed": len(changed)})
}
