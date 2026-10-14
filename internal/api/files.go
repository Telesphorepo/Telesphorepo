package api

import (
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/files"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Browse, delete and upload repo files, and the audit log (spec §13).

const (
	maxUploadFiles  = 50   // files per manual upload
	maxUploadFields = 16   // text fields per manual upload
	maxFieldBytes   = 1024 // bytes per text field
)

// tree lists the direct children of a folder: ?path=<folder>&after=<cursor>&limit= ->
// {items: [Node], next}. A folder's name in next ends in "/".
func (a *API) tree(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.Read, true)
	if !ok {
		return
	}
	q := r.URL.Query()
	dir, err := files.Folder(q.Get("path"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid path")
		return
	}
	items, next, err := a.files.Tree(r.Context(), repo, dir, q.Get("after"), httpx.Limit(r))
	if errors.Is(err, files.ErrInvalid) {
		httpx.Error(w, http.StatusBadRequest, "invalid cursor")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	if items == nil {
		items = []files.Node{}
	}
	httpx.WriteJSON(w, http.StatusOK, list[files.Node]{items, cursor(next)})
}

// deleteFiles deletes one file (?path=) or a folder (?prefix=) with a publish session for the
// repo or an admin UI session: 200 {deleted: <count>}. A missing file is 404; an empty folder
// deletes 0 files.
func (a *API) deleteFiles(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := a.auth.DeleteGate(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	q := r.URL.Query()
	file, prefix := q.Get("path"), q.Get("prefix")
	if (file == "") == (prefix == "") {
		httpx.Error(w, http.StatusBadRequest, "give path (one file) or prefix (a folder)")
		return
	}
	var n int
	var err error
	target := file
	if file != "" {
		if err = a.files.DeletePath(r.Context(), repo, file); err == nil {
			n = 1
		}
	} else {
		target = strings.TrimSuffix(prefix, "/") + "/"
		n, err = a.files.DeletePrefix(r.Context(), repo, prefix)
	}
	switch {
	case errors.Is(err, files.ErrInvalid):
		httpx.Error(w, http.StatusBadRequest, "invalid path")
		return
	case errors.Is(err, files.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "file not found")
		return
	}
	if n > 0 {
		a.record(r, p, audit.Delete, repo, target, map[string]any{"count": n})
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"deleted": n})
}

// upload takes a multipart upload of files with a publish session for the repo and hands it to
// the format's ManualUploader: 201 {files: [Uploaded]}. The files together may not exceed
// max_upload_bytes.
func (a *API) upload(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.Write, true)
	if !ok {
		return
	}
	up, ok := format.Get(repo.Format).(format.ManualUploader)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "repos of format "+repo.Format+" take no uploads from the web UI")
		return
	}
	limit := a.cfg().MaxUploadBytes
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20) // the files plus the multipart framing
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "send multipart/form-data")
		return
	}
	var ups []format.UploadFile
	defer func() {
		for _, f := range ups {
			f.Spool.Close()
		}
	}()
	fields := map[string]string{}
	var total int64
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			uploadReadFail(w, r, err)
			return
		}
		if part.FileName() == "" {
			if len(fields) >= maxUploadFields {
				httpx.Error(w, http.StatusBadRequest, "too many form fields")
				return
			}
			b, err := io.ReadAll(io.LimitReader(part, maxFieldBytes+1))
			if err != nil {
				uploadReadFail(w, r, err)
				return
			}
			if len(b) > maxFieldBytes {
				httpx.Error(w, http.StatusBadRequest, "form field "+part.FormName()+" is too long")
				return
			}
			fields[part.FormName()] = string(b)
			continue
		}
		if len(ups) >= maxUploadFiles {
			httpx.Error(w, http.StatusBadRequest, "upload at most "+strconv.Itoa(maxUploadFiles)+" files at a time")
			return
		}
		name := path.Base(strings.ReplaceAll(part.FileName(), `\`, "/"))
		if !files.ValidPath(name) {
			httpx.Error(w, http.StatusBadRequest, "invalid file name")
			return
		}
		sp, err := a.assets.Spool(part, limit-total)
		if err != nil {
			uploadReadFail(w, r, err)
			return
		}
		total += sp.Size
		ups = append(ups, format.UploadFile{Name: name, Spool: sp})
	}
	if len(ups) == 0 {
		httpx.Error(w, http.StatusBadRequest, "no files in the upload")
		return
	}
	var uid int64
	if p != nil {
		uid = p.UserID
	}
	res, err := up.Upload(r.Context(), repo, ups, fields, uid)
	a.recordUploads(r, p, repo, res)
	var ue *format.UploadError
	if errors.As(err, &ue) {
		httpx.Error(w, ue.Status, ue.Message)
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string][]format.Uploaded{"files": res})
}

// recordUploads writes each file a manual upload stored to the audit log.
func (a *API) recordUploads(r *http.Request, p *auth.Principal, repo *repos.Repo, res []format.Uploaded) {
	for _, u := range res {
		if u.Status == format.UploadUnchanged {
			continue
		}
		a.record(r, p, audit.Upload, repo, u.Path, map[string]any{"size": u.Size, "sha256": u.SHA256, "manual": true})
	}
}

// uploadReadFail answers an error while the upload is read.
func uploadReadFail(w http.ResponseWriter, r *http.Request, err error) {
	var mbe *http.MaxBytesError
	switch {
	case errors.Is(err, assets.ErrTooLarge) || errors.As(err, &mbe):
		httpx.Error(w, http.StatusRequestEntityTooLarge, "upload too large")
	case errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "multipart"):
		httpx.Error(w, http.StatusBadRequest, "incomplete or malformed multipart body")
	default:
		fail(w, r, err)
	}
}

// listAudit returns the audit log, newest first (admin): ?after=<id>&limit=&action=&repo=<name>.
func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	after, ok := idCursor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := audit.Filter{Action: q.Get("action")}
	if name := q.Get("repo"); name != "" {
		repo, err := a.repos.ByName(r.Context(), name)
		if errors.Is(err, repos.ErrNotFound) {
			httpx.WriteJSON(w, http.StatusOK, list[audit.Entry]{Items: []audit.Entry{}})
			return
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		f.RepoID = repo.ID
	}
	items, next, err := a.audit.List(r.Context(), f, after, httpx.Limit(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list[audit.Entry]{items, idNext(next)})
}
