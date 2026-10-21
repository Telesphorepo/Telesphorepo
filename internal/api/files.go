package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/files"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// Browse, delete and upload repo files.

const (
	maxUploadFiles  = 500  // files per manual upload or scan
	maxUploadFields = 16   // text fields per manual upload
	maxFieldBytes   = 1024 // bytes per text field
)

// tree lists the direct children of a folder: ?path=<folder>&after=<cursor>&limit= ->
// {items: [Node], next}. A folder's name in next ends in "/".
func (a *API) tree(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	q := r.URL.Query()
	dir, err := repopath.Folder(q.Get("path"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid path")
		return
	}
	var items []files.Node
	var next string
	if repo.Kind == repos.KindGroup {
		// A group lists the merged files of the members that the caller reads, as its reads serve them.
		var members []*repos.Repo
		if members, err = a.access.GroupMembers(r.Context(), r, c.P, repo); err == nil {
			items, next, err = a.files.GroupTree(r.Context(), members, string(dir), q.Get("after"), httpx.Limit(r))
		}
	} else {
		items, next, err = a.files.Tree(r.Context(), repo, string(dir), q.Get("after"), httpx.Limit(r))
	}
	if errors.Is(err, files.ErrInvalid) {
		httpx.Error(w, r, http.StatusBadRequest, "invalid cursor")
		return
	}
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if items == nil {
		items = []files.Node{}
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[files.Node]{Items: items, Next: route.Cursor(next)})
}

// deleteFiles deletes files (paths) and folders (prefixes) of the repo through the delete
// pipeline, for a person who holds delete there, with a UI session or a publish session for the
// repo: {paths, prefixes} -> 200 {deleted: <file count>}. The two lists hold 1 to 1000 items
// together. A bad path or prefix answers 400 and deletes nothing. A file that is gone already
// counts as done. One audit event goes to each file and each folder that lost files.
func (a *API) deleteFiles(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	var in struct {
		Paths    []string `json:"paths"`
		Prefixes []string `json:"prefixes"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	if n := len(in.Paths) + len(in.Prefixes); n == 0 || n > maxBulk {
		httpx.Error(w, r, http.StatusBadRequest, "paths and prefixes must hold 1 to "+strconv.Itoa(maxBulk)+" items together")
		return
	}
	rm := assets.Removal{Repo: repo, Origin: format.OriginOf(c.P, assets.SourceUI)}
	for _, f := range in.Paths {
		p, err := repopath.Parse(f)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid path "+strconv.Quote(clipName(f)))
			return
		}
		rm.Paths = append(rm.Paths, p)
	}
	for _, prefix := range in.Prefixes {
		dir, err := repopath.Folder(prefix)
		if err != nil || dir == "" {
			httpx.Error(w, r, http.StatusBadRequest, "invalid prefix "+strconv.Quote(clipName(prefix)))
			return
		}
		if !slices.Contains(rm.Prefixes, dir) {
			rm.Prefixes = append(rm.Prefixes, dir)
		}
	}
	n, err := a.assets.Remove(r.Context(), rm)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"deleted": n})
}

// maxDeleteScan is the most files a delete plan walks under the folders of a delete.
const maxDeleteScan = 100_000

// deleteNote is one note of a delete plan: a DeleteNote of the format with the count of the files
// of the delete that its glob matches.
type deleteNote struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
	One   string `json:"one"`
	Many  string `json:"many"`
}

// deletePlan says what a delete of files (paths) and folders (prefixes) removes, and deletes
// nothing: {paths, prefixes} -> 200 {files, more, notes: [{text, count, one, many}]}. files counts
// the files that exist; more is true when the folders hold more files than the walk reads. Each
// note is a DeleteNote of the format with the count of the files its glob matches (notes with no
// match are left out). It walks the assets of each folder by the (repo_id, path) index.
func (a *API) deletePlan(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	var in struct {
		Paths    []string `json:"paths"`
		Prefixes []string `json:"prefixes"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	if n := len(in.Paths) + len(in.Prefixes); n == 0 || n > maxBulk {
		httpx.Error(w, r, http.StatusBadRequest, "paths and prefixes must hold 1 to "+strconv.Itoa(maxBulk)+" items together")
		return
	}
	var notes []format.DeleteNote
	if f := a.formats.Get(repo.Format); f != nil {
		notes = f.UI.DeleteNotes
	}
	counts := make([]int, len(notes))
	files, scanned, more := 0, 0, false
	seen := map[string]bool{}
	count := func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		files++
		for i, n := range notes {
			if format.Glob(n.Match, p) {
				counts[i]++
			}
		}
	}
	for _, raw := range in.Paths {
		p, err := repopath.Parse(raw)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid path "+strconv.Quote(clipName(raw)))
			return
		}
		if _, err := a.assets.Get(r.Context(), repo.ID, string(p)); errors.Is(err, assets.ErrNotFound) {
			continue
		} else if err != nil {
			route.Fail(w, r, err)
			return
		}
		count(string(p))
	}
	for _, raw := range in.Prefixes {
		dir, err := repopath.Folder(raw)
		if err != nil || dir == "" {
			httpx.Error(w, r, http.StatusBadRequest, "invalid prefix "+strconv.Quote(clipName(raw)))
			return
		}
		after := ""
		for !more {
			items, next, err := a.assets.List(r.Context(), repo.ID, string(dir), after, 1000)
			if err != nil {
				route.Fail(w, r, err)
				return
			}
			for _, it := range items {
				count(it.Path)
			}
			scanned += len(items)
			if next == "" {
				break
			}
			if scanned >= maxDeleteScan {
				more = true
			}
			after = next
		}
	}
	out := []deleteNote{}
	for i, n := range notes {
		if counts[i] > 0 {
			out = append(out, deleteNote{Text: n.Text, Count: counts[i], One: n.One, Many: n.Many})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Files int          `json:"files"`
		More  bool         `json:"more"`
		Notes []deleteNote `json:"notes"`
	}{files, more, out})
}

// uploadSpec returns the upload spec of the repo's format, or answers 400.
func (a *API) uploadSpec(w http.ResponseWriter, r *http.Request, repo *repos.Repo) (*format.UploadSpec, bool) {
	f := a.formats.Get(repo.Format)
	if f == nil || f.Upload == nil {
		httpx.Error(w, r, http.StatusBadRequest, "repos of format "+repo.Format+" take no uploads from the web UI")
		return nil, false
	}
	return f.Upload, true
}

// readRule returns the read rule of up whose glob matches path, or nil.
func readRule(up *format.UploadSpec, path string) *format.ReadRule {
	for i := range up.Reads {
		if format.Glob(up.Reads[i].Glob, path) {
			return &up.Reads[i]
		}
	}
	return nil
}

// planOut is the answer of a plan: the plan, its JSON lists never null.
func planOut(p *format.Plan) *format.Plan {
	p.Normalize()
	return p
}

// uploadPlan works out what an upload of files would store, with the format's Upload.Plan, and
// stores nothing: 200 Plan. The multipart body holds the part "plan" first, a JSON object
// {fields, folder, files: [{path, size}]} (paths relative to the drop, at most 500), then one part
// per file whose path matches a read rule of the format (filename: its path), holding at most
// the rule's max_bytes from the start of the file. It takes the credential of the upload.
func (a *API) uploadPlan(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	up, ok := a.uploadSpec(w, r, repo)
	if !ok {
		return
	}
	limit := settings.MaxUploadSize.Get(a.settings.Current())
	r.Body = http.MaxBytesReader(w, r.Body, limit+2<<20+int64(maxUploadFiles)*1024)
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "send multipart/form-data")
		return
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "plan" {
		httpx.Error(w, r, http.StatusBadRequest, `the first part must be "plan"`)
		return
	}
	var req struct {
		Fields map[string]string `json:"fields"`
		Folder string            `json:"folder"`
		Files  []struct {
			Path string `json:"path"`
			Size int64  `json:"size"`
		} `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(part, 2<<20)).Decode(&req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "the plan part is not valid JSON: "+err.Error())
		return
	}
	if len(req.Files) == 0 || len(req.Files) > maxUploadFiles {
		httpx.Error(w, r, http.StatusBadRequest, "a plan takes 1 to "+strconv.Itoa(maxUploadFiles)+" files")
		return
	}
	if len(req.Fields) > maxUploadFields {
		httpx.Error(w, r, http.StatusBadRequest, "too many form fields")
		return
	}
	for k, v := range req.Fields {
		if len(v) > maxFieldBytes {
			httpx.Error(w, r, http.StatusBadRequest, "form field "+k+" is too long")
			return
		}
	}
	folder, err := repopath.Folder(req.Folder)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid folder")
		return
	}
	in := format.PlanInput{Fields: req.Fields, Folder: string(folder)}
	if in.Fields == nil {
		in.Fields = map[string]string{}
	}
	index := map[string]int{}
	for _, f := range req.Files {
		p := strings.TrimLeft(strings.ReplaceAll(f.Path, `\`, "/"), "/")
		if _, err := repopath.Parse(p); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid file name "+strconv.Quote(clipName(f.Path)))
			return
		}
		if _, dup := index[p]; dup {
			httpx.Error(w, r, http.StatusBadRequest, p+" is in the upload twice")
			return
		}
		if f.Size < 0 {
			httpx.Error(w, r, http.StatusBadRequest, "invalid size of "+p)
			return
		}
		index[p] = len(in.Files)
		in.Files = append(in.Files, format.PlanFile{Path: p, Size: f.Size})
	}
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
		rel, isFile := partPath(part)
		i, known := index[rel]
		rule := readRule(up, rel)
		if !isFile || !known || rule == nil {
			httpx.Error(w, r, http.StatusBadRequest, "a plan takes only the first bytes of the listed files that the format reads")
			return
		}
		b, err := io.ReadAll(io.LimitReader(part, rule.MaxBytes+1))
		if err != nil {
			uploadReadFail(w, r, err)
			return
		}
		if int64(len(b)) > rule.MaxBytes || int64(len(b)) > in.Files[i].Size {
			httpx.Error(w, r, http.StatusBadRequest, "the part of "+rel+" is longer than the plan reads")
			return
		}
		if total += int64(len(b)); total > limit {
			httpx.Error(w, r, http.StatusRequestEntityTooLarge, "upload too large")
			return
		}
		in.Files[i].Head = b
	}
	plan, err := up.Plan(r.Context(), repo, in)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, planOut(plan))
}

// upload takes a multipart upload of files with a publish session for the repo: the format's
// fields first, then the files, each with its path relative to the drop as its filename. It runs
// the format's Upload.Plan on the files (the first bytes of each file that a read rule matches),
// refuses 400 when a field or an item has a problem or no item takes a file, then hands the
// files of the items to Upload.Store, which commits them through the asset pipeline (source
// "ui"): 201 {files: [Uploaded]}. The files together may not exceed max_upload_size.
func (a *API) upload(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	up, ok := a.uploadSpec(w, r, repo)
	if !ok {
		return
	}
	ups, fields, ok := a.readUpload(w, r)
	defer closeUploads(ups)
	if !ok {
		return
	}
	in := format.PlanInput{Fields: fields}
	for _, f := range ups {
		pf := format.PlanFile{Path: f.path, Size: f.sp.Size}
		if rule := readRule(up, f.path); rule != nil {
			head, err := headOf(f.sp, rule.MaxBytes)
			if err != nil {
				route.Fail(w, r, err)
				return
			}
			pf.Head = head
		}
		in.Files = append(in.Files, pf)
	}
	plan, err := up.Plan(r.Context(), repo, in)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if msg := plan.Blocked(); msg != "" {
		httpx.Error(w, r, http.StatusBadRequest, msg)
		return
	}
	if len(plan.Items) == 0 {
		msg := up.Empty
		if msg == "" {
			msg = "the upload has no file to store"
		}
		httpx.Error(w, r, http.StatusBadRequest, msg)
		return
	}
	files := map[string]*assets.Spooled{}
	for _, f := range ups {
		files[f.path] = f.sp
	}
	rs, err := up.Store(r.Context(), repo, plan, files, format.OriginOf(c.P, assets.SourceUI))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string][]format.Uploaded{"files": format.UploadedOf(rs)})
}

// headOf returns the first max bytes of sp.
func headOf(sp *assets.Spooled, max int64) ([]byte, error) {
	rd, err := sp.Reader()
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(rd, max))
}

// uploadFile is one spooled file of an upload with its path relative to the drop.
type uploadFile struct {
	path string
	sp   *assets.Spooled
}

// readUpload reads the multipart body of an upload: text fields first, then files. A file's
// path is its filename as sent (the browser's path inside the dropped folder); every path must
// be a valid repo path and appear once. The files together may not exceed max_upload_size. On
// failure it has answered and returns ok=false; the caller closes the files either way.
func (a *API) readUpload(w http.ResponseWriter, r *http.Request) ([]uploadFile, map[string]string, bool) {
	limit := settings.MaxUploadSize.Get(a.settings.Current())
	// The files plus the multipart framing.
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20+int64(maxUploadFiles)*1024)
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "send multipart/form-data")
		return nil, nil, false
	}
	var ups []uploadFile
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
			return ups, nil, false
		}
		rel, isFile := partPath(part)
		if !isFile {
			if len(fields) >= maxUploadFields {
				httpx.Error(w, r, http.StatusBadRequest, "too many form fields")
				return ups, nil, false
			}
			b, err := io.ReadAll(io.LimitReader(part, maxFieldBytes+1))
			if err != nil {
				uploadReadFail(w, r, err)
				return ups, nil, false
			}
			if len(b) > maxFieldBytes {
				httpx.Error(w, r, http.StatusBadRequest, "form field "+part.FormName()+" is too long")
				return ups, nil, false
			}
			fields[part.FormName()] = string(b)
			continue
		}
		if len(ups) >= maxUploadFiles {
			httpx.Error(w, r, http.StatusBadRequest, "upload at most "+strconv.Itoa(maxUploadFiles)+" files at a time")
			return ups, nil, false
		}
		if _, err := repopath.Parse(rel); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid file name "+strconv.Quote(clipName(rel)))
			return ups, nil, false
		}
		if seen[rel] {
			httpx.Error(w, r, http.StatusBadRequest, rel+" is in the upload twice")
			return ups, nil, false
		}
		seen[rel] = true
		sp, err := a.assets.Spool(part, limit-total)
		if err != nil {
			uploadReadFail(w, r, err)
			return ups, nil, false
		}
		total += sp.Size
		ups = append(ups, uploadFile{path: rel, sp: sp})
	}
	if len(ups) == 0 {
		httpx.Error(w, r, http.StatusBadRequest, "no files in the upload")
		return nil, nil, false
	}
	return ups, fields, true
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

func closeUploads(ups []uploadFile) {
	for _, f := range ups {
		f.sp.Close()
	}
}

// uploadReadFail answers an error while the upload is read.
func uploadReadFail(w http.ResponseWriter, r *http.Request, err error) {
	var mbe *http.MaxBytesError
	switch {
	case errors.Is(err, assets.ErrTooLarge) || errors.As(err, &mbe):
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, "upload too large")
	case errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "multipart"):
		httpx.Error(w, r, http.StatusBadRequest, "incomplete or malformed multipart body")
	default:
		route.Fail(w, r, err)
	}
}

// fileDetails returns one file with its details and its CI build record: ?path= -> File. In a
// group it is the file of the first member that the caller reads and that stores it (the file a
// group read serves when no member fetches from a remote), with that member in repo.
func (a *API) fileDetails(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	p := r.URL.Query().Get("path")
	var f *files.File
	var err error
	if repo.Kind == repos.KindGroup {
		var members []*repos.Repo
		if members, err = a.access.GroupMembers(r.Context(), r, c.P, repo); err == nil {
			err = files.ErrNotFound
			for _, m := range members {
				if f, err = a.files.Details(r.Context(), m, p); err == nil {
					f.Repo = m.Name
					break
				}
				if !errors.Is(err, files.ErrNotFound) {
					break
				}
			}
		}
	} else {
		f, err = a.files.Details(r.Context(), repo, p)
	}
	switch {
	case errors.Is(err, files.ErrInvalid):
		httpx.Error(w, r, http.StatusBadRequest, "invalid path")
	case errors.Is(err, files.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "file not found")
	case err != nil:
		route.Fail(w, r, err)
	default:
		httpx.WriteJSON(w, http.StatusOK, f)
	}
}
