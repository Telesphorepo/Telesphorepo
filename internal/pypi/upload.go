package pypi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// The legacy upload API (the one twine, uv publish and poetry publish speak): a multipart POST
// with the file in the field content and the fields :action=file_upload, protocol_version, name,
// version, filetype, pyversion and the digests. The server reads the name and the version from
// the file's own metadata; a form name or version that disagrees is refused, a digest that does
// not match too, and any other field is ignored.

// maxField is the largest form field other than content that the upload reads.
const maxField = 1 << 20

// fail answers an error of the upload in the form its client prints: the body holds the message
// (twine prints it with --verbose, uv always). A client that asks for JSON (the Telesphorepo
// CLI) gets the JSON error of a format route.
func fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		httpx.Error(w, r, status, msg)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	io.WriteString(w, msg+"\n")
}

// failErr answers err of an upload: a refusal or a bad distribution with its status, else 500.
func (h *Handler) failErr(w http.ResponseWriter, r *http.Request, err error) {
	var ref *assets.Refusal
	var bd badDist
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &ref):
		fail(w, r, ref.Status, ref.Message)
	case errors.As(err, &bd):
		fail(w, r, http.StatusBadRequest, bd.Error())
	case errors.Is(err, assets.ErrTooLarge), errors.As(err, &mbe):
		fail(w, r, http.StatusRequestEntityTooLarge, "upload too large (the server setting max_upload_size)")
	default:
		h.log.Error("pypi upload failed", "path", r.URL.Path, "err", err)
		fail(w, r, http.StatusInternalServerError, "internal error")
	}
}

// uploaded is the answer of an upload.
type uploaded struct {
	Project  string `json:"project"`
	Version  string `json:"version"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
}

// upload takes one distribution file: the multipart form of the legacy API, or with
// ?upload=<id>&sha256=<hex>[&size=<n>]&filename=<name> and no body the file of a resumable upload
// of the same publish session (/api/repos/{name}/uploads), which this request finishes. The file
// is 201 when new, 200 when the same bytes are there, 409 when the file name holds other bytes.
func (h *Handler) upload(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var sp *assets.Spooled
	var filename string
	var want wantMeta
	if id := r.URL.Query().Get("upload"); id != "" {
		filename = r.URL.Query().Get("filename")
		if sp = h.finishUpload(w, r, c, id); sp == nil {
			return
		}
	} else {
		var ok bool
		if sp, filename, want, ok = h.readForm(w, r); !ok {
			return
		}
	}
	defer sp.Close()
	rs, out, err := h.ingest(r.Context(), c.Repo, filename, sp, format.OriginOf(c.P, assets.SourcePut), want)
	switch {
	case err != nil:
		h.failErr(w, r, err)
	case rs[0].Status == assets.Unchanged:
		httpx.WriteJSON(w, http.StatusOK, out)
	default:
		httpx.WriteJSON(w, http.StatusCreated, out)
	}
}

// wantMeta are the name and the version that an upload form names ("" for none).
type wantMeta struct{ name, version string }

// readForm reads the multipart form of the legacy API: the content file into the spool, the other
// fields into memory. On failure it has answered and returns false.
func (h *Handler) readForm(w http.ResponseWriter, r *http.Request) (*assets.Spooled, string, wantMeta, bool) {
	max := h.assets.MaxUploadSize()
	if r.ContentLength > max+16*maxField {
		fail(w, r, http.StatusRequestEntityTooLarge, "upload too large (the server setting max_upload_size)")
		return nil, "", wantMeta{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, max+16*maxField)
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "multipart/form-data" {
		fail(w, r, http.StatusBadRequest, "the upload is a multipart/form-data POST with the file in the field content")
		return nil, "", wantMeta{}, false
	}
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, r, http.StatusBadRequest, "the upload is a multipart/form-data POST with the file in the field content")
		return nil, "", wantMeta{}, false
	}
	fields := map[string]string{}
	var sp *assets.Spooled
	var filename string
	cleanup := func() {
		if sp != nil {
			sp.Close()
		}
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanup()
			h.failErr(w, r, badDist{"the multipart body does not parse: " + err.Error()})
			return nil, "", wantMeta{}, false
		}
		field := part.FormName()
		if field == "content" {
			if sp != nil {
				cleanup()
				fail(w, r, http.StatusBadRequest, "the upload has two content files; upload one file per request")
				return nil, "", wantMeta{}, false
			}
			filename = part.FileName()
			if sp, err = h.assets.Spool(part, max); err != nil {
				h.failErr(w, r, err)
				return nil, "", wantMeta{}, false
			}
			continue
		}
		b, err := io.ReadAll(io.LimitReader(part, maxField+1))
		if err == nil && len(b) > maxField {
			err = fmt.Errorf("the field %s is larger than %d bytes", field, maxField)
		}
		if err != nil {
			cleanup()
			h.failErr(w, r, badDist{err.Error()})
			return nil, "", wantMeta{}, false
		}
		if _, dup := fields[field]; !dup {
			fields[field] = string(b)
		}
	}
	refuse := func(msg string) (*assets.Spooled, string, wantMeta, bool) {
		cleanup()
		fail(w, r, http.StatusBadRequest, msg)
		return nil, "", wantMeta{}, false
	}
	if a := fields[":action"]; a != "" && a != "file_upload" {
		return refuse(fmt.Sprintf(":action %s is not supported; this server takes file_upload", a))
	}
	if v := fields["protocol_version"]; v != "" && v != "1" {
		return refuse(fmt.Sprintf("protocol_version %s is not supported; this server takes 1", v))
	}
	if sp == nil {
		return refuse("the upload has no file in the field content")
	}
	if d := strings.ToLower(strings.TrimSpace(fields["sha256_digest"])); d != "" && d != sp.SHA256 {
		return refuse("the sha256_digest of the form does not match the file")
	}
	if d := strings.ToLower(strings.TrimSpace(fields["md5_digest"])); d != "" && d != sp.MD5 {
		return refuse("the md5_digest of the form does not match the file")
	}
	return sp, filename, wantMeta{strings.TrimSpace(fields["name"]), strings.TrimSpace(fields["version"])}, true
}

// finishUpload ends the resumable upload id of the caller's publish session with the sha256 and
// size of the query, and returns its file. It answers the error itself and returns nil.
func (h *Handler) finishUpload(w http.ResponseWriter, r *http.Request, c *route.Call, id string) *assets.Spooled {
	q := r.URL.Query()
	digest := strings.ToLower(q.Get("sha256"))
	if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
		fail(w, r, http.StatusBadRequest, "give sha256, the 64 hex digits of the SHA-256 of the whole file")
		return nil
	}
	if q.Get("filename") == "" {
		fail(w, r, http.StatusBadRequest, "give filename, the name of the distribution file")
		return nil
	}
	size := int64(-1)
	if s := q.Get("size"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 0 {
			fail(w, r, http.StatusBadRequest, "invalid size")
			return nil
		}
		size = n
	}
	sp, err := h.host.Uploads.Finish(r.Context(), id, c.P, c.Repo, digest, size)
	switch {
	case errors.Is(err, assets.ErrUploadUnknown):
		fail(w, r, http.StatusNotFound, err.Error())
	case errors.Is(err, assets.ErrDigest):
		fail(w, r, http.StatusBadRequest, err.Error())
	case err != nil:
		h.failErr(w, r, err)
	default:
		return sp
	}
	return nil
}

// ingest stores the distribution file filename of sp in repo: it reads the core metadata, checks
// it against the file name and want, and commits the file at packages/<project>/<filename> with,
// for a wheel, its METADATA at the same path plus .metadata, in one commit. Under the path locks,
// admit keeps the same bytes and refuses other bytes at a file name that exists (409);
// onCommitted writes the rows. A client error is an *assets.Refusal or a badDist. The legacy
// upload, the web UI upload, the import and the demo data share it.
func (h *Handler) ingest(ctx context.Context, repo *repos.Repo, filename string, sp *assets.Spooled, o assets.Origin, want wantMeta) ([]*assets.Result, uploaded, error) {
	var out uploaded
	f, err := parseFilename(filename)
	if err != nil {
		return nil, out, bad("%v", err)
	}
	md, err := readMetadata(f, sp)
	if err != nil {
		return nil, out, err
	}
	if err := md.check(f, filename); err != nil {
		return nil, out, err
	}
	if want.name != "" && normalize(want.name) != normalize(md.name) {
		return nil, out, bad("the form names project %s, but %s is a file of %s", want.name, filename, md.name)
	}
	if want.version != "" {
		if v, err := version.ParsePEP440(want.version); err != nil || v.String() != md.version.String() {
			return nil, out, bad("the form names version %s, but %s is a file of version %s", want.version, filename, md.version)
		}
	}
	project := normalize(md.name)
	p, err := repopath.Parse("packages/" + project + "/" + filename)
	if err != nil {
		return nil, out, bad("invalid file name")
	}
	m := &distMeta{project: project, display: md.name, filename: filename, md: md}
	writes := []assets.Write{{Repo: repo, Path: p, Body: assets.Body{Spool: sp}, Origin: o, Meta: m}}
	if f.kind == kindWheel {
		msp := blobs.Bytes(md.raw)
		m.metaSHA = msp.SHA256
		g := o
		g.Source = assets.SourceGenerated
		writes = append(writes, assets.Write{Repo: repo, Path: p + ".metadata", Body: assets.Body{Spool: msp}, Origin: g, Meta: sidecar{}})
	}
	out = uploaded{Project: project, Version: md.version.String(), Filename: filename, SHA256: sp.SHA256}
	rs, err := h.assets.CommitAll(ctx, writes)
	if err != nil {
		return nil, out, err
	}
	return rs, out, nil
}
