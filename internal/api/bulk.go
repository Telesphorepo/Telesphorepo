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
func listOf(w http.ResponseWriter, field string, xs []string) ([]string, bool) {
	if len(xs) == 0 || len(xs) > maxBulk {
		httpx.Error(w, http.StatusBadRequest, field+" must hold 1 to "+strconv.Itoa(maxBulk)+" items")
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
			httpx.Error(w, d.Status, d.Message)
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
	names, ok := listOf(w, "names", in.Names)
	if !ok {
		return
	}
	if in.AnonymousRead == nil {
		httpx.Error(w, http.StatusBadRequest, "give anonymous_read")
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
	names, ok := listOf(w, "usernames", in.Usernames)
	if !ok {
		return
	}
	if in.Disabled == nil {
		httpx.Error(w, http.StatusBadRequest, "give disabled")
		return
	}
	if *in.Disabled && slices.Contains(names, p.Username) {
		httpx.Error(w, http.StatusBadRequest, "You cannot disable your own account.")
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
				httpx.Error(w, http.StatusForbidden, "you can change only people whose permissions you all hold: "+name)
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
func pairsIn(w http.ResponseWriter, users, repoNames []string) ([]string, []string, bool) {
	us, ok := listOf(w, "users", users)
	if !ok {
		return nil, nil, false
	}
	rs, ok := listOf(w, "repos", repoNames)
	if !ok {
		return nil, nil, false
	}
	if len(us)*len(rs) > maxBulk {
		httpx.Error(w, http.StatusBadRequest, "give at most "+strconv.Itoa(maxBulk)+" pairs of user and repository")
		return nil, nil, false
	}
	return us, rs, true
}

// setPermissions sets the repository permission set of every pair of users and repos:
// {users, repos, perms} -> 200 {changed: n}. perms [] removes the pairs. The caller holds
// repo.access on every repo (403) and adds or removes only permissions they hold there (403).
// An unknown permission ID or a permission without one it needs answers 400. An unknown user or
// repo answers 404 and changes nothing. The auth service records one grant.set event per pair
// whose set changed, with the old and the new set.
func (a *API) setPermissions(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	var in struct {
		Users []string `json:"users"`
		Repos []string `json:"repos"`
		Perms []string `json:"perms"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	users, names, ok := pairsIn(w, in.Users, in.Repos)
	if !ok {
		return
	}
	if _, err := perm.Current().Parse(perm.ScopeRepo, in.Perms); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if !a.canEach(w, r, p, names, perm.RepoAccess) {
		return
	}
	changed, err := a.auth.SetPermissions(r.Context(), p, users, names, in.Perms)
	if errors.Is(err, auth.ErrNotHeld) {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"changed": len(changed)})
}
