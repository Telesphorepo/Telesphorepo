package api

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Write routes that change a set take a list (spec §10, bulk actions). Each route checks every
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

// canEach checks that p holds need (or one of them) on every repo of names: 404 for an unknown
// name, 403 for a repo without the permission.
func (a *API) canEach(w http.ResponseWriter, r *http.Request, p *auth.Principal, names []string, need auth.RepoPerm) bool {
	if p.Perms.Has(auth.PermRepos) {
		return true
	}
	for _, name := range names {
		repo, err := a.repos.ByName(r.Context(), name)
		if errors.Is(err, repos.ErrNotFound) {
			err = &repos.MissingError{Kind: "repository", Name: name}
		}
		if err != nil {
			fail(w, r, err)
			return false
		}
		set, err := a.auth.RepoPerms(r.Context(), p, repo)
		if err != nil {
			fail(w, r, err)
			return false
		}
		if !set.Any(need) {
			httpx.Error(w, http.StatusForbidden, "permission required on "+repo.Name+": "+strings.Join(need.Names(), " or "))
			return false
		}
	}
	return true
}

// setReposPublic makes the repos of names public or private (settings on each):
// {names, anonymous_read} -> 200 {changed: n}. An unknown name answers 404 and changes nothing.
func (a *API) setReposPublic(w http.ResponseWriter, r *http.Request) {
	p, ok := a.session(w, r)
	if !ok {
		return
	}
	var in struct {
		Names         []string `json:"names"`
		AnonymousRead *bool    `json:"anonymous_read"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
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
	if !a.canEach(w, r, p, names, auth.RepoSettings) {
		return
	}
	changed, err := a.repos.SetAnonymousRead(r.Context(), names, *in.AnonymousRead)
	if err != nil {
		fail(w, r, err)
		return
	}
	for _, repo := range changed {
		a.record(r, p, audit.RepoUpdate, repo, repo.Name, map[string]any{"fields": []string{"anonymous_read"}, "anonymous_read": repo.AnonymousRead})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"changed": len(changed)})
}

// deleteRepos deletes the repos of names with all their files and blobs (repos): {names} ->
// 200 {deleted: [names], files: n}. It first checks that every repo exists (404) and that none
// is locked (409); then nothing is deleted. A delete removes blobs, so the set is not one
// transaction: the repos go one after another, and the first failure stops the rest with 500
// {deleted: [names done], error}.
func (a *API) deleteRepos(w http.ResponseWriter, r *http.Request) {
	p, ok := a.can(w, r, auth.PermRepos)
	if !ok {
		return
	}
	var in struct {
		Names []string `json:"names"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	names, ok := listOf(w, "names", in.Names)
	if !ok {
		return
	}
	ctx := r.Context()
	all := make([]*repos.Repo, 0, len(names))
	for _, name := range names {
		repo, err := a.repos.ByName(ctx, name)
		if errors.Is(err, repos.ErrNotFound) {
			err = &repos.MissingError{Kind: "repository", Name: name}
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		if repo.LockedBy != "" {
			httpx.Error(w, http.StatusConflict, repo.Name+": "+repo.LockedBy)
			return
		}
		all = append(all, repo)
	}
	deleted := make([]string, 0, len(all))
	files := 0
	for _, repo := range all {
		n, err := a.files.DeleteRepo(ctx, repo)
		files += n
		if err != nil {
			slog.ErrorContext(ctx, "delete repo", "repo", repo.Name, "err", err)
			httpx.WriteJSON(w, http.StatusInternalServerError, map[string]any{
				"deleted": deleted, "error": "The server could not delete " + repo.Name + ". The server log shows the cause."})
			return
		}
		a.rules.Forget(repo.ID) // the rows cascaded with the repo; an id can be used again
		// The repo row is gone, so the event keeps only its name (in the detail).
		a.audit.Record(ctx, r, audit.Event{Actor: auth.ActorOf(p), Action: audit.RepoDelete,
			Repo: repo.Name, Target: repo.Name, Detail: map[string]any{"count": n}})
		deleted = append(deleted, repo.Name)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "files": files})
}

// setUsersDisabled disables or enables the users of usernames (people): {usernames, disabled} ->
// 200 {changed: n}. An unknown name answers 404 and changes nothing. The caller cannot disable
// their own account, and changes only people whose permissions they all hold (403). A disabled
// user's sessions end. The last enabled person with People stays enabled (409).
func (a *API) setUsersDisabled(w http.ResponseWriter, r *http.Request) {
	p, ok := a.can(w, r, auth.PermPeople)
	if !ok {
		return
	}
	var in struct {
		Usernames []string `json:"usernames"`
		Disabled  *bool    `json:"disabled"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
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
			fail(w, r, err)
			return
		}
		if ok, err := a.auth.Covers(r.Context(), p, u); err != nil || !ok {
			if err != nil {
				fail(w, r, err)
			} else {
				httpx.Error(w, http.StatusForbidden, "you can change only people whose permissions you all hold: "+name)
			}
			return
		}
	}
	changed, err := a.auth.SetDisabled(r.Context(), names, *in.Disabled)
	if err != nil {
		fail(w, r, err)
		return
	}
	for _, u := range changed {
		a.record(r, p, audit.UserUpdate, nil, u.Username, map[string]any{"disabled": u.Disabled})
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
// access on every repo (403) and adds or removes only permissions they hold there (403).
// publish without download, or pipelines without publish, answers 400. An unknown user or repo
// answers 404 and changes nothing. One permission.set event per pair whose set changed, with the
// old and the new set.
func (a *API) setPermissions(w http.ResponseWriter, r *http.Request) {
	p, ok := a.session(w, r)
	if !ok {
		return
	}
	var in struct {
		Users []string `json:"users"`
		Repos []string `json:"repos"`
		Perms []string `json:"perms"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	users, names, ok := pairsIn(w, in.Users, in.Repos)
	if !ok {
		return
	}
	perms, err := auth.ParseRepoPerms(in.Perms)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "unknown permission; the repository permissions are "+strings.Join(auth.RepoPermNames, ", "))
		return
	}
	if !perms.Valid() {
		httpx.Error(w, http.StatusBadRequest, auth.ErrPermDeps.Error())
		return
	}
	if !a.canEach(w, r, p, names, auth.RepoAccess) {
		return
	}
	changed, err := a.auth.SetPermissions(r.Context(), p, users, names, perms)
	if errors.Is(err, auth.ErrNotHeld) {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	for _, g := range changed {
		a.record(r, p, audit.PermissionSet, g.Repo, g.Username, map[string]any{"old": g.Old.Names(), "new": g.New.Names()})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"changed": len(changed)})
}
