package api

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

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

// setReposPublic makes the repos of names public or private (admin): {names, anonymous_read} ->
// 200 {changed: n}. An unknown name answers 404 and changes nothing.
func (a *API) setReposPublic(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
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

// deleteRepos deletes the repos of names with all their files and blobs (admin): {names} ->
// 200 {deleted: [names], files: n}. It first checks that every repo exists (404) and that none
// is locked (409); then nothing is deleted. A delete removes blobs, so the set is not one
// transaction: the repos go one after another, and the first failure stops the rest with 500
// {deleted: [names done], error}.
func (a *API) deleteRepos(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
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

// setUsersDisabled disables or enables the users of usernames (admin): {usernames, disabled} ->
// 200 {changed: n}. An unknown name answers 404 and changes nothing. The caller cannot disable
// their own account. A disabled user's sessions end.
func (a *API) setUsersDisabled(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
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

// setPermissions sets level on every pair of users and repos (admin): {users, repos, level} ->
// 200 {changed: n}. An unknown user or repo answers 404 and changes nothing.
func (a *API) setPermissions(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
	if !ok {
		return
	}
	var in struct {
		Users []string `json:"users"`
		Repos []string `json:"repos"`
		Level string   `json:"level"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	users, names, ok := pairsIn(w, in.Users, in.Repos)
	if !ok {
		return
	}
	var lvl auth.Level
	switch in.Level {
	case "read":
		lvl = auth.Read
	case "write":
		lvl = auth.Write
	default:
		httpx.Error(w, http.StatusBadRequest, `level must be "read" or "write"`)
		return
	}
	changed, err := a.auth.SetPermissions(r.Context(), users, names, lvl)
	if err != nil {
		fail(w, r, err)
		return
	}
	for _, g := range changed {
		a.record(r, p, audit.PermissionSet, g.Repo, g.Username, map[string]any{"level": in.Level})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"changed": len(changed)})
}

// deletePermissions removes the level of every pair of users and repos (admin): {users, repos}
// -> 200 {changed: n}. An unknown user or repo answers 404 and changes nothing.
func (a *API) deletePermissions(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
	if !ok {
		return
	}
	var in struct {
		Users []string `json:"users"`
		Repos []string `json:"repos"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	users, names, ok := pairsIn(w, in.Users, in.Repos)
	if !ok {
		return
	}
	changed, err := a.auth.DeletePermissions(r.Context(), users, names)
	if err != nil {
		fail(w, r, err)
		return
	}
	for _, g := range changed {
		a.record(r, p, audit.PermissionDelete, g.Repo, g.Username, nil)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"changed": len(changed)})
}
