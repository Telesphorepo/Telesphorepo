package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// The kernel's byte pipeline in the core module: the repo delete operation, the jobs of the
// blob store, the resumable uploads and the rebuild queue, and the routes of resumable uploads
// and repo operations.

const (
	gcMaxWait      = 10 * time.Minute // the blob GC runs at least this often
	expireEvery    = 10 * time.Minute // how often expired uploads are deleted
	resumeAtStart  = 100              // running repo deletes resumed per start
	digestHexchars = 64
)

// registerPipeline adds the op type "delete" and the jobs core.blob-gc, core.upload-sessions,
// core.rebuild and core.ops.
func (a *API) registerPipeline(x *module.Extender) {
	x.OpType(ops.Type{ID: ops.TypeDelete, Label: "Repository delete", Message: deleteMessage, Resume: a.resumeDelete, Clear: a.clearDelete})
	x.Job(jobs.Job{Name: "core.blob-gc", Run: a.collectBlobs})
	x.Job(jobs.Job{Name: "core.upload-sessions", Every: expireEvery, Tick: func(ctx context.Context) error {
		return a.uploads.Expire(ctx)
	}})
	x.Job(jobs.Job{Name: "core.rebuild",
		Start: func(ctx context.Context) error { return a.h.Rebuilds.Start(ctx) },
		Run:   func(ctx context.Context) { a.h.Rebuilds.Stop(ctx) }})
	x.Job(jobs.Job{Name: "core.ops", Start: a.resumeDeletes})
}

// collectBlobs runs the blob GC until ctx ends: after each wait of blobs.gc_grace (at most
// gcMaxWait, at least a second) it deletes the blobs that no asset referenced for longer than the
// grace.
func (a *API) collectBlobs(ctx context.Context) {
	for {
		grace := settings.BlobsGCGrace.Get(a.settings.Current())
		select {
		case <-ctx.Done():
			return
		case <-time.After(min(max(grace, time.Second), gcMaxWait)):
		}
		n, err := a.h.Blobs.GC(ctx, grace)
		switch {
		case err != nil && ctx.Err() == nil:
			a.h.Log.Error("blob GC failed", "err", err)
		case n > 0:
			a.h.Log.Info("blob GC deleted unused blobs", "blobs", n)
		}
	}
}

// deleteMessage is the text of a repo delete.
func deleteMessage(op *ops.Op) string {
	if op.State == ops.Failed {
		return "A delete of the repository failed (" + op.Error + "). Resume it, or stop it and keep the files, on the repository's page."
	}
	return "The repository is being deleted."
}

// deleteRepos deletes the repos of names with all their files (repositories.all): {names} ->
// 200 {deleted: [names], files: n}. It first checks that every repo exists (404) and that none
// has an operation (409); then nothing is deleted yet. Each repo then gets a delete operation,
// which removes its files batch by batch (writes answer 409 meanwhile) and then the repo row. A
// failure keeps the operation as failed: the repo can be resumed or cleared, and the answer is
// 500 {deleted: [names done], error}.
func (a *API) deleteRepos(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Names []string `json:"names"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	names, ok := listOf(w, r, "names", in.Names)
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
			route.Fail(w, r, err)
			return
		}
		if repo.Op != nil {
			httpx.Error(w, r, http.StatusConflict, "You cannot delete "+repo.Name+" now. "+a.ops.Message(repo.Op))
			return
		}
		all = append(all, repo)
	}
	deleted := make([]string, 0, len(all))
	files := 0
	for _, repo := range all {
		var op *ops.Op
		err := a.h.DB.Tx(ctx, func(tx *sql.Tx) error {
			var err error
			op, err = a.ops.Begin(ctx, tx, repo.ID, ops.TypeDelete, "core", "", ops.Running, c.P.UserID)
			return err
		})
		if errors.Is(err, ops.ErrBusy) {
			httpx.WriteProblem(w, r, httpx.Blank(http.StatusConflict, repo.Name+": "+err.Error()).With(map[string]any{"deleted": deleted}))
			return
		}
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		n, err := a.runDelete(ctx, repo, op)
		files += n
		if err != nil {
			httpx.WriteProblem(w, r, httpx.Blank(http.StatusInternalServerError, "The server could not delete "+repo.Name+": "+err.Error()).With(map[string]any{"deleted": deleted}))
			return
		}
		deleted = append(deleted, repo.Name)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "files": files})
}

// runDelete removes every file of repo under its delete operation op, with progress, then deletes
// the repo row (the operation row goes with it) and records repo.delete. A failure sets the
// operation to failed with the error. It runs to the end when the client goes away.
func (a *API) runDelete(ctx context.Context, repo *repos.Repo, op *ops.Op) (int, error) {
	ctx = context.WithoutCancel(ctx)
	total := repo.FileCount
	n, err := a.assets.Remove(ctx, assets.Removal{Repo: repo, Prefixes: []repopath.Path{""},
		Origin: assets.Origin{Source: assets.SourceGenerated},
		Progress: func(done int) {
			_ = a.ops.Progress(ctx, op.ID, int64(done), max(total, int64(done)))
		}})
	if err == nil {
		err = a.repos.DeleteRow(ctx, repo.ID)
	}
	if err != nil {
		a.h.Log.Error("delete repo", "repo", repo.Name, "err", err)
		_ = a.ops.Fail(ctx, op.ID, err)
		return n, err
	}
	// The repo row is gone, so the event keeps only its name.
	_ = a.audit.Record(ctx, audit.Event{Action: audit.RepoDelete, Repo: repo.Name, Target: repo.Name, Detail: map[string]any{"count": n}})
	return n, nil
}

// resumeDelete runs a failed repo delete again (ops.Type Resume).
func (a *API) resumeDelete(ctx context.Context, op *ops.Op) error {
	repo, err := a.repos.ByID(ctx, op.RepoID)
	if err != nil {
		return err
	}
	if err := a.ops.SetState(ctx, op.ID, ops.Running, nil); err != nil {
		return err
	}
	_ = a.audit.Record(ctx, audit.Event{Action: audit.RepoDeleteResume, RepoID: repo.ID, Repo: repo.Name, Target: repo.Name})
	_, err = a.runDelete(ctx, repo, op)
	return err
}

// clearDelete ends a failed repo delete: the repo stays with the files that are left (ops.Type
// Clear).
func (a *API) clearDelete(ctx context.Context, op *ops.Op) error {
	repo, err := a.repos.ByID(ctx, op.RepoID)
	if err != nil {
		return err
	}
	if err := a.ops.End(ctx, nil, op.ID, "core"); err != nil {
		return err
	}
	_ = a.audit.Record(ctx, audit.Event{Action: audit.RepoDeleteClear, RepoID: repo.ID, Repo: repo.Name, Target: repo.Name,
		Detail: map[string]any{"files": repo.FileCount}})
	return nil
}

// resumeDeletes continues the repo deletes that a stop interrupted, each as a task of the core
// module (the core.ops job).
func (a *API) resumeDeletes(ctx context.Context) error {
	running, err := a.ops.Running(ctx, ops.TypeDelete, resumeAtStart)
	if err != nil {
		return err
	}
	for _, op := range running {
		repo, err := a.repos.ByID(ctx, op.RepoID)
		if err != nil {
			return err
		}
		a.h.Go("repo delete", func(ctx context.Context) { _, _ = a.runDelete(ctx, repo, op) })
	}
	return nil
}

// operationOf returns the operation of repo {name}; on failure it has answered.
func (a *API) operationOf(w http.ResponseWriter, r *http.Request) (*ops.Op, *ops.Type, bool) {
	repo, err := a.repos.ByName(r.Context(), r.PathValue("name"))
	if err != nil {
		route.Fail(w, r, err)
		return nil, nil, false
	}
	if repo.Op == nil {
		httpx.Error(w, r, http.StatusConflict, ops.ErrNone.Error())
		return nil, nil, false
	}
	t := a.ops.Type(repo.Op.Type)
	if t == nil {
		httpx.Error(w, r, http.StatusConflict, "the repository has an operation of an unknown type")
		return nil, nil, false
	}
	return repo.Op, t, true
}

// resumeOperation resumes the failed operation of repo {name} (repositories.all): 200 {ok: true}
// when it finished, 409 when the operation is not failed or its type cannot resume, or the error.
func (a *API) resumeOperation(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	op, t, ok := a.operationOf(w, r)
	if !ok {
		return
	}
	switch {
	case t.Resume == nil:
		httpx.Error(w, r, http.StatusConflict, "this operation cannot be resumed here")
		return
	case op.State != ops.Failed:
		httpx.Error(w, r, http.StatusConflict, "the operation has not failed")
		return
	}
	if err := t.Resume(r.Context(), op); err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// clearOperation ends the failed operation of repo {name} without finishing it (repositories.all):
// 204, or 409 when the operation is not failed or its type is ended by its own module (e.g. a
// Nexus mirror ends at the cut-over).
func (a *API) clearOperation(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	op, t, ok := a.operationOf(w, r)
	if !ok {
		return
	}
	switch {
	case t.Clear == nil:
		httpx.Error(w, r, http.StatusConflict, "this operation is ended by the "+op.Owner+" module, not here")
		return
	case op.State != ops.Failed:
		httpx.Error(w, r, http.StatusConflict, "the operation has not failed")
		return
	}
	if err := t.Clear(r.Context(), op); err != nil {
		route.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- resumable uploads ---

// startUpload starts a resumable upload into repo {name} for the caller's publish session: 201
// {id, received: 0, expires_at}.
func (a *API) startUpload(w http.ResponseWriter, r *http.Request, c *route.Call) {
	s, err := a.uploads.Start(r.Context(), c.P, c.Repo)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, s)
}

// uploadFail answers an error of a resumable upload.
func uploadFail(w http.ResponseWriter, r *http.Request, err error) {
	var re *assets.RangeError
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &re):
		w.Header().Set("Range", "bytes=0-"+strconv.FormatInt(max(re.Received-1, 0), 10))
		httpx.WriteProblem(w, r, httpx.Blank(http.StatusRequestedRangeNotSatisfiable, err.Error()).With(map[string]any{"received": re.Received}))
	case errors.Is(err, assets.ErrUploadUnknown):
		httpx.Error(w, r, http.StatusNotFound, err.Error())
	case errors.Is(err, assets.ErrDigest):
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
	case errors.Is(err, assets.ErrTooLarge), errors.As(err, &mbe):
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, "upload too large")
	default:
		route.Fail(w, r, err)
	}
}

var contentRangeRE = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\*|\d+)$`)

// appendUpload appends the body to upload {id} at the offset of its Content-Range header
// ("bytes <from>-<to>/*"; without the header, at the end): 202 {received}, or 416 {received} on
// a wrong offset.
func (a *API) appendUpload(w http.ResponseWriter, r *http.Request, c *route.Call) {
	offset := int64(-1)
	if cr := r.Header.Get("Content-Range"); cr != "" {
		m := contentRangeRE.FindStringSubmatch(strings.TrimSpace(cr))
		if m == nil {
			httpx.Error(w, r, http.StatusBadRequest, `Content-Range must be "bytes <from>-<to>/*"`)
			return
		}
		offset, _ = strconv.ParseInt(m[1], 10, 64)
	}
	received, err := a.uploads.Append(r.Context(), r.PathValue("id"), c.P, c.Repo, offset, r.Body)
	if err != nil {
		uploadFail(w, r, err)
		return
	}
	w.Header().Set("Range", "bytes=0-"+strconv.FormatInt(max(received-1, 0), 10))
	httpx.WriteJSON(w, http.StatusAccepted, map[string]int64{"received": received})
}

// finishUpload ends upload {id} with ?path=<path>&sha256=<hex>[&size=<n>] and commits it at the
// path with source "put": the answers of a format PUT (201, 200, or the format's refusal), 400
// when the bytes do not match the digest or the size (the upload is deleted).
func (a *API) finishUpload(w http.ResponseWriter, r *http.Request, c *route.Call) {
	q := r.URL.Query()
	p, err := repopath.Parse(q.Get("path"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid path")
		return
	}
	digest := strings.ToLower(q.Get("sha256"))
	if len(digest) != digestHexchars || strings.Trim(digest, "0123456789abcdef") != "" {
		httpx.Error(w, r, http.StatusBadRequest, "give sha256, the 64 hex digits of the SHA-256 of the whole file")
		return
	}
	size := int64(-1)
	if s := q.Get("size"); s != "" {
		if size, err = strconv.ParseInt(s, 10, 64); err != nil || size < 0 {
			httpx.Error(w, r, http.StatusBadRequest, "invalid size")
			return
		}
	}
	sp, err := a.uploads.Finish(r.Context(), r.PathValue("id"), c.P, c.Repo, digest, size)
	if err != nil {
		uploadFail(w, r, err)
		return
	}
	defer sp.Close()
	res, err := a.assets.Commit(r.Context(), assets.Write{Repo: c.Repo, Path: p, Body: assets.Body{Spool: sp},
		Origin: format.OriginOf(c.P, assets.SourcePut)})
	format.WriteResult(w, r, res, err)
}

// cancelUpload deletes upload {id} and its bytes: 204.
func (a *API) cancelUpload(w http.ResponseWriter, r *http.Request, c *route.Call) {
	if err := a.uploads.Cancel(r.Context(), r.PathValue("id"), c.P, c.Repo); err != nil {
		uploadFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
