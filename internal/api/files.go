package api

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"slices"
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
	maxUploadFiles  = 500  // files per manual upload or scan
	maxUploadFields = 16   // text fields per manual upload
	maxFieldBytes   = 1024 // bytes per text field
)

// tree lists the direct children of a folder: ?path=<folder>&after=<cursor>&limit= ->
// {items: [Node], next}. A folder's name in next ends in "/".
func (a *API) tree(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.RepoDownload, true)
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

// deleteFiles deletes files (paths) and folders (prefixes) of the repo for a person who holds
// delete there, with a UI session or a publish session for the repo: {paths, prefixes} -> 200
// {deleted: <file count>}. The two lists hold 1 to 1000 items together. A bad path or prefix answers 400 and deletes nothing. The
// paths go in one transaction, then each folder in batches. A file that is gone already counts
// as done. One audit event goes to each file and each folder that lost files.
func (a *API) deleteFiles(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := a.auth.DeleteGate(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	var in struct {
		Paths    []string `json:"paths"`
		Prefixes []string `json:"prefixes"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	if n := len(in.Paths) + len(in.Prefixes); n == 0 || n > maxBulk {
		httpx.Error(w, http.StatusBadRequest, "paths and prefixes must hold 1 to "+strconv.Itoa(maxBulk)+" items together")
		return
	}
	for _, f := range in.Paths {
		if !files.ValidPath(f) {
			httpx.Error(w, http.StatusBadRequest, "invalid path "+strconv.Quote(clipName(f)))
			return
		}
	}
	dirs := make([]string, 0, len(in.Prefixes))
	for _, prefix := range in.Prefixes {
		dir, err := files.Folder(prefix)
		if err != nil || dir == "" {
			httpx.Error(w, http.StatusBadRequest, "invalid prefix "+strconv.Quote(clipName(prefix)))
			return
		}
		if !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	n := 0
	if len(in.Paths) > 0 {
		deleted, err := a.files.DeletePaths(r.Context(), repo, in.Paths)
		if err != nil {
			fail(w, r, err)
			return
		}
		for _, f := range deleted {
			a.record(r, p, audit.Delete, repo, f, map[string]any{"count": 1})
		}
		n += len(deleted)
	}
	for _, dir := range dirs {
		c, err := a.files.DeletePrefix(r.Context(), repo, dir)
		if c > 0 {
			a.record(r, p, audit.Delete, repo, dir, map[string]any{"count": c})
		}
		n += c
		if err != nil {
			fail(w, r, err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"deleted": n})
}

// upload takes a multipart upload of files with a publish session for the repo and hands it to
// the format's ManualUploader: 201 {files: [Uploaded]}. The files together may not exceed
// max_upload_size.
func (a *API) upload(w http.ResponseWriter, r *http.Request) {
	repo, p, up, ups, fields, ok := a.readUpload(w, r)
	defer closeUploads(ups)
	if !ok {
		return
	}
	res, err := up.Upload(r.Context(), repo, ups, fields, format.ByOf(p))
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

// scanUpload looks at the files of a dropped folder with the format's ManualUploader and stores
// nothing: 200 {found: [Found], ignored: [path]}. It takes the same credential and body as
// upload, so the client sends the confirmed files to upload afterwards.
func (a *API) scanUpload(w http.ResponseWriter, r *http.Request) {
	repo, _, up, ups, _, ok := a.readUpload(w, r)
	defer closeUploads(ups)
	if !ok {
		return
	}
	found, err := up.Scan(r.Context(), repo, ups)
	var ue *format.UploadError
	if errors.As(err, &ue) {
		httpx.Error(w, ue.Status, ue.Message)
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	taken := map[string]bool{}
	if found == nil {
		found = []format.Found{}
	}
	for i := range found {
		if found[i].Files == nil {
			found[i].Files = []string{}
		}
		if found[i].Problems == nil {
			found[i].Problems = []string{}
		}
		for _, f := range found[i].Files {
			taken[f] = true
		}
	}
	ignored := []string{}
	for _, f := range ups {
		if !taken[f.Path] {
			ignored = append(ignored, f.Path)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Found   []format.Found `json:"found"`
		Ignored []string       `json:"ignored"`
	}{found, ignored})
}

// readUpload authorizes a manual upload or scan of repo {name} (a publish session of the repo)
// and reads its multipart body: text fields first, then files. A file's relative path is its
// filename as sent (the browser's path inside the dropped folder); every path must be a valid
// repo path and appear once. The files together may not exceed max_upload_size. On failure it
// has answered and returns ok=false; the caller closes the files either way.
func (a *API) readUpload(w http.ResponseWriter, r *http.Request) (*repos.Repo, *auth.Principal, format.ManualUploader, []format.UploadFile, map[string]string, bool) {
	repo, p, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.RepoPublish, true)
	if !ok {
		return nil, nil, nil, nil, nil, false
	}
	up, ok := format.Get(repo.Format).(format.ManualUploader)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "repos of format "+repo.Format+" take no uploads from the web UI")
		return nil, nil, nil, nil, nil, false
	}
	limit := a.cfg().MaxUploadBytes
	// The files plus the multipart framing.
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20+int64(maxUploadFiles)*1024)
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "send multipart/form-data")
		return nil, nil, nil, nil, nil, false
	}
	var ups []format.UploadFile
	fields := map[string]string{}
	seen := map[string]bool{}
	var total int64
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			uploadReadFail(w, r, err)
			return nil, nil, nil, ups, nil, false
		}
		rel, isFile := partPath(part)
		if !isFile {
			if len(fields) >= maxUploadFields {
				httpx.Error(w, http.StatusBadRequest, "too many form fields")
				return nil, nil, nil, ups, nil, false
			}
			b, err := io.ReadAll(io.LimitReader(part, maxFieldBytes+1))
			if err != nil {
				uploadReadFail(w, r, err)
				return nil, nil, nil, ups, nil, false
			}
			if len(b) > maxFieldBytes {
				httpx.Error(w, http.StatusBadRequest, "form field "+part.FormName()+" is too long")
				return nil, nil, nil, ups, nil, false
			}
			fields[part.FormName()] = string(b)
			continue
		}
		if len(ups) >= maxUploadFiles {
			httpx.Error(w, http.StatusBadRequest, "upload at most "+strconv.Itoa(maxUploadFiles)+" files at a time")
			return nil, nil, nil, ups, nil, false
		}
		if !files.ValidPath(rel) {
			httpx.Error(w, http.StatusBadRequest, "invalid file name "+strconv.Quote(clipName(rel)))
			return nil, nil, nil, ups, nil, false
		}
		if seen[rel] {
			httpx.Error(w, http.StatusBadRequest, rel+" is in the upload twice")
			return nil, nil, nil, ups, nil, false
		}
		seen[rel] = true
		sp, err := a.assets.Spool(part, limit-total)
		if err != nil {
			uploadReadFail(w, r, err)
			return nil, nil, nil, ups, nil, false
		}
		total += sp.Size
		ups = append(ups, format.UploadFile{Name: path.Base(rel), Path: rel, Spool: sp})
	}
	if len(ups) == 0 {
		httpx.Error(w, http.StatusBadRequest, "no files in the upload")
		return nil, nil, nil, nil, nil, false
	}
	return repo, p, up, ups, fields, true
}

// partPath returns the relative path of a file part: the filename parameter as sent, with
// backslashes as slashes and without leading slashes. multipart.Part.FileName keeps only the
// base name, so the header is parsed here. isFile is false for a text field.
func partPath(part *multipart.Part) (rel string, isFile bool) {
	_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if err != nil {
		return "", part.FileName() != ""
	}
	name, ok := params["filename"]
	if !ok {
		return "", false
	}
	return strings.TrimLeft(strings.ReplaceAll(name, `\`, "/"), "/"), true
}

// clipName shortens a file name for an error message.
func clipName(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

func closeUploads(ups []format.UploadFile) {
	for _, f := range ups {
		f.Spool.Close()
	}
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

// listAudit returns the audit log, newest first (activity): ?after=<id>&limit=&action=&repo=<name>.
func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermActivity); !ok {
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

// fileDetails returns one file with its details and build provenance: ?path= -> File.
func (a *API) fileDetails(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.RepoDownload, true)
	if !ok {
		return
	}
	f, err := a.files.Details(r.Context(), repo, r.URL.Query().Get("path"))
	switch {
	case errors.Is(err, files.ErrInvalid):
		httpx.Error(w, http.StatusBadRequest, "invalid path")
	case errors.Is(err, files.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "file not found")
	case err != nil:
		fail(w, r, err)
	default:
		httpx.WriteJSON(w, http.StatusOK, f)
	}
}
