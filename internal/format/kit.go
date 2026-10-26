package format

import (
	"errors"
	"io"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// Kit is what the routes of a format use: the read path and the asset pipeline.
type Kit struct {
	Reader *assets.Reader
	Assets *assets.Pipeline
}

// Standard mounts the default routes of format name: GET and HEAD /<name>/{repo}/{path...}
// through the read path, and PUT through Commit with source "put" (Kit.Put).
func Standard(name string, r *route.Router, k Kit) {
	r.Handle("GET /"+name+"/{repo}/{path...}", route.FormatRead(name), k.Get)
	r.Handle("PUT /"+name+"/{repo}/{path...}", route.FormatWrite(name), k.Put)
}

// Get serves the {path...} of the request from repo c.Repo through the read path.
func (k Kit) Get(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, err := httpx.RepoPath(r, "path")
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid path")
		return
	}
	k.Reader.Serve(w, r, c, p)
}

// Put stores the body of the request at its {path...} in repo c.Repo through Commit with source
// "put" and the precondition of its headers (PreconditionOf): 201 when the file is new, 200 when
// it replaced a file or has the same bytes, 400 for a precondition that a write does not take,
// 412 when the precondition is false, the refusal of the format (400, 404 or 409), or 413 above
// max_upload_size.
func (k Kit) Put(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, err := httpx.RepoPath(r, "path")
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid path")
		return
	}
	pc, err := PreconditionOf(r)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
		return
	}
	sp, ok := k.SpoolBody(w, r)
	if !ok {
		return
	}
	defer sp.Close()
	res, err := k.Assets.Commit(r.Context(), assets.Write{Repo: c.Repo, Path: p, Body: assets.Body{Spool: sp},
		Origin: OriginOf(c.P, assets.SourcePut), Precondition: pc})
	WriteResult(w, r, res, err)
}

// SpoolBody spools the request body under max_upload_size. On failure it has answered (413, or
// 400 for a body that ended early) and returns false.
func (k Kit) SpoolBody(w http.ResponseWriter, r *http.Request) (*assets.Spooled, bool) {
	max := k.Assets.MaxUploadSize()
	if r.ContentLength > max {
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, "upload too large")
		return nil, false
	}
	sp, err := k.Assets.Spool(http.MaxBytesReader(w, r.Body, max), max)
	if err != nil {
		var mbe *http.MaxBytesError
		switch {
		case errors.Is(err, assets.ErrTooLarge) || errors.As(err, &mbe):
			httpx.Error(w, r, http.StatusRequestEntityTooLarge, "upload too large")
		case errors.Is(err, io.ErrUnexpectedEOF):
			httpx.Error(w, r, http.StatusBadRequest, "incomplete request body")
		default:
			route.Fail(w, r, err)
		}
		return nil, false
	}
	return sp, true
}

// WriteResult answers a single-file write: 201 for a new file, 200 for a replaced or unchanged
// one, with the ETag of the file that the path holds now; the status of a refusal, else 500.
func WriteResult(w http.ResponseWriter, r *http.Request, res *assets.Result, err error) {
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if res.Asset != nil {
		w.Header().Set("ETag", `"`+res.Asset.SHA256+`"`)
	}
	if res.Status == assets.Created {
		w.WriteHeader(http.StatusCreated)
		return
	}
	w.WriteHeader(http.StatusOK)
}
