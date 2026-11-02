package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// Deletes of files in a group: the group holds no files, so a delete there deletes in its members.

// holderJSON is one member of a holders answer: whether the caller may delete in it, and when not,
// why (the refusal of the access engine).
type holderJSON struct {
	Repo   string `json:"repo"`
	Delete bool   `json:"delete"`
	Reason string `json:"reason,omitempty"`
}

// heldJSON is one picked file or folder of a holders answer with the members that hold it, in
// read order.
type heldJSON struct {
	Path  string   `json:"path"`
	Repos []string `json:"repos"`
}

// fileHolders answers which members of the group hold each picked file and folder, among the
// members the caller reads through the group, and which of them the caller may delete in:
// {paths, prefixes} -> {members: [{repo, delete, reason}], paths: [{path, repos}], prefixes:
// [{path, repos}]}. members lists, in read order, each member that holds a picked item, with the
// access engine's Delete decision (repo.delete, the member's operation). A file costs one point
// lookup per member, a folder one seek to its first file per member. A repository that is not a
// group is its own one member.
func (a *API) fileHolders(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Paths    []string `json:"paths"`
		Prefixes []string `json:"prefixes"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	rm, ok := removalOf(w, r, in.Paths, in.Prefixes)
	if !ok {
		return
	}
	ctx := r.Context()
	members := []*repos.Repo{c.Repo}
	if c.Repo.Kind == repos.KindGroup {
		var err error
		if members, err = a.access.GroupMembers(ctx, r, c.P, c.Repo); err != nil {
			route.Fail(w, r, err)
			return
		}
	}
	held := map[int64]bool{}
	out := struct {
		Members  []holderJSON `json:"members"`
		Paths    []heldJSON   `json:"paths"`
		Prefixes []heldJSON   `json:"prefixes"`
	}{Members: []holderJSON{}, Paths: []heldJSON{}, Prefixes: []heldJSON{}}
	for _, p := range rm.Paths {
		h := heldJSON{Path: string(p), Repos: []string{}}
		for _, m := range members {
			_, err := a.assets.Get(ctx, m.ID, string(p))
			if errors.Is(err, assets.ErrNotFound) {
				continue
			}
			if err != nil {
				route.Fail(w, r, err)
				return
			}
			h.Repos, held[m.ID] = append(h.Repos, m.Name), true
		}
		out.Paths = append(out.Paths, h)
	}
	for _, dir := range rm.Prefixes {
		h := heldJSON{Path: string(dir), Repos: []string{}}
		for _, m := range members {
			first, _, err := a.assets.List(ctx, m.ID, string(dir), "", 1)
			if err != nil {
				route.Fail(w, r, err)
				return
			}
			if len(first) > 0 {
				h.Repos, held[m.ID] = append(h.Repos, m.Name), true
			}
		}
		out.Prefixes = append(out.Prefixes, h)
	}
	ctx, err := a.auth.WithGrants(ctx, c.P)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	for _, m := range members {
		if !held[m.ID] {
			continue
		}
		d := a.access.Decide(ctx, r, c.P, m, access.Delete, perm.RepoDelete)
		if d.Status == http.StatusInternalServerError {
			route.Fail(w, r, errors.New(d.Message))
			return
		}
		hj := holderJSON{Repo: m.Name, Delete: d.Allowed}
		if !d.Allowed {
			hj.Reason = d.Message
		}
		out.Members = append(out.Members, hj)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// memberDeleteJSON is the delete in one member of a group delete: the files it removed so far,
// and while it runs past delete.wait the id of its delete of files in that member
// (GET /api/repos/{member}/files/delete/{id}); error when it failed.
type memberDeleteJSON struct {
	Repo    string `json:"repo"`
	Deleted int64  `json:"deleted"`
	ID      string `json:"id,omitempty"`
	Error   string `json:"error,omitempty"`
}

// deleteInMembers deletes the files and folders of rm in the members of the group that names
// lists, each through the delete of files of that member: the access engine's Delete decision
// on the member (its repo.delete and its operation), a delete of files of the member with its
// progress, its audit events. names must be members that the caller reads through the group (400
// otherwise). A member that refuses keeps its files and is listed in kept with the refusal; when
// every one refuses, the answer is the first refusal. -> 200 {deleted, repos: [memberDeleteJSON],
// kept: [{repo, reason}]} when every delete ends within delete.wait, else 202 with the id of each
// delete that runs on.
func (a *API) deleteInMembers(w http.ResponseWriter, r *http.Request, c *route.Call, rm assets.Removal, names []string) {
	group := c.Repo
	if len(names) == 0 {
		httpx.Error(w, r, http.StatusBadRequest, "name the repositories of "+group.Name+" to delete in (members)")
		return
	}
	if len(names) > maxBulk {
		httpx.Error(w, r, http.StatusBadRequest, "members must hold 1 to "+strconv.Itoa(maxBulk)+" items")
		return
	}
	members, err := a.access.GroupMembers(r.Context(), r, c.P, group)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	ctx, err := a.auth.WithGrants(r.Context(), c.P)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	for _, n := range names {
		if !slices.ContainsFunc(members, func(m *repos.Repo) bool { return m.Name == n }) {
			httpx.Error(w, r, http.StatusBadRequest, strconv.Quote(clipName(n))+" is not a repository of "+group.Name+" that you read")
			return
		}
	}
	var targets []*repos.Repo
	kept := []holderJSON{}
	var refused *access.Decision
	for _, m := range members {
		if !slices.Contains(names, m.Name) {
			continue
		}
		d := a.access.Decide(ctx, r, c.P, m, access.Delete, perm.RepoDelete)
		switch {
		case d.Status == http.StatusInternalServerError:
			route.Fail(w, r, errors.New(d.Message))
			return
		case !d.Allowed:
			kept = append(kept, holderJSON{Repo: m.Name, Reason: d.Message})
			if refused == nil {
				refused = &d
			}
		default:
			targets = append(targets, m)
		}
	}
	if len(targets) == 0 {
		route.Refuse(w, r, *refused)
		return
	}
	// Reserve a delete of files in each member first, so that a member at its limit starts none.
	tasks := make([]*fileDelete, 0, len(targets))
	for _, m := range targets {
		t, ok := a.fileDeletes.start(m.ID)
		if !ok {
			for _, t := range tasks {
				a.fileDeletes.drop(t)
			}
			httpx.Error(w, r, http.StatusTooManyRequests, m.Name+" runs "+strconv.Itoa(maxRepoDeletes)+" deletes of files already; try again when one ends")
			return
		}
		tasks = append(tasks, t)
	}
	// Each task keeps the request's values (the actor of the audit events) without its cancel.
	rctx := context.WithoutCancel(r.Context())
	for i, m := range targets {
		t, mrm := tasks[i], rm
		mrm.Repo = m
		mrm.Progress = func(n int) { t.deleted.Store(int64(n)) }
		a.h.Go("files delete", func(context.Context) {
			n, err := a.assets.Remove(rctx, mrm)
			a.fileDeletes.finish(t, n, err)
		})
	}
	wait := time.After(settings.DeleteWait.Get(a.settings.Current()))
	running := false
	for _, t := range tasks {
		select {
		case <-t.done:
		case <-wait:
			running = true
		case <-r.Context().Done():
			return
		}
		if running {
			break
		}
	}
	out := struct {
		Deleted int64              `json:"deleted"`
		Repos   []memberDeleteJSON `json:"repos"`
		Kept    []holderJSON       `json:"kept"`
	}{Repos: []memberDeleteJSON{}, Kept: kept}
	status := http.StatusOK
	for i, t := range tasks {
		md := memberDeleteJSON{Repo: targets[i].Name}
		select {
		case <-t.done:
			if t.err != nil {
				md.Error = t.err.Error()
			}
		default:
			md.ID, status = t.id, http.StatusAccepted
		}
		md.Deleted = t.deleted.Load()
		out.Deleted += md.Deleted
		out.Repos = append(out.Repos, md)
	}
	httpx.WriteJSON(w, status, out)
}
