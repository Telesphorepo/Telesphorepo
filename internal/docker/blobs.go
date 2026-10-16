package docker

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// getBlob serves a blob of the image, with ranges; a GET may redirect to the store.
func (f *Format) getBlob(w http.ResponseWriter, r *http.Request, c *route.Call, t target) {
	hex, err := parseDigest(t.ref)
	if err != nil {
		fail(w, r, err)
		return
	}
	a, err := f.h.Reader.Resolve(r.Context(), r, c.P, c.Repo, repopath.Path(linkPath(t.image, hex)))
	switch {
	case errors.Is(err, assets.ErrRemoteFailed):
		writeOCI(w, ociErr(http.StatusBadGateway, codeUnknown, "The remote repository did not answer."))
		return
	case err != nil:
		fail(w, r, err)
		return
	case a == nil:
		writeOCI(w, ociErr(http.StatusNotFound, codeBlobUnknown, "blob unknown: "+t.ref))
		return
	}
	w.Header().Set("Docker-Content-Digest", "sha256:"+a.SHA256)
	f.h.Reader.ServeAsset(w, r, a, assets.ServeOptions{})
}

// uploadHeaders sets the headers of an upload answer: Location, Docker-Upload-UUID and Range
// (0-<last byte>, "0-0" for an empty upload).
func uploadHeaders(w http.ResponseWriter, t target, id string, received int64) {
	h := w.Header()
	h.Set("Location", location(t.name, "blobs/uploads", id))
	h.Set("Docker-Upload-UUID", id)
	h.Set("Range", "0-"+strconv.FormatInt(max(received-1, 0), 10))
	h.Set("Content-Length", "0")
}

// blobCreated answers a stored blob: 201 with its Location and digest.
func blobCreated(w http.ResponseWriter, t target, hex string) {
	h := w.Header()
	h.Set("Location", location(t.name, actBlobs, "sha256:"+hex))
	h.Set("Docker-Content-Digest", "sha256:"+hex)
	h.Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

// commitLink stores the link of a blob in the image: from a body, or from a blob the server has
// (a mount).
func (f *Format) commitLink(r *http.Request, c *route.Call, t target, hex string, body assets.Body) error {
	_, err := f.h.Assets.Commit(r.Context(), assets.Write{Repo: c.Repo, Path: repopath.Path(linkPath(t.image, hex)), Body: body,
		Origin: format.OriginOf(c.P.For(c.Repo.Name), assets.SourcePut), Meta: &writeMeta{kind: kindLink}})
	return err
}

// startUpload answers POST blobs/uploads/: a mount (mount= and from=) when the caller reads the
// source and it stores the blob, a monolithic upload (digest= with the body), else a new upload
// session (202).
func (f *Format) startUpload(w http.ResponseWriter, r *http.Request, c *route.Call, t target) {
	q := r.URL.Query()
	if m := q.Get("mount"); m != "" {
		if hex, ok := f.mountable(r, c, m, q.Get("from")); ok {
			if err := f.commitLink(r, c, t, hex, assets.Body{Blob: hex}); err != nil {
				fail(w, r, err)
				return
			}
			blobCreated(w, t, hex)
			return
		}
	} else if d := q.Get("digest"); d != "" {
		hex, err := parseDigest(d)
		if err != nil {
			fail(w, r, err)
			return
		}
		sp, oe := f.spool(w, r)
		if oe != nil {
			writeOCI(w, oe)
			return
		}
		defer sp.Close()
		if sp.SHA256 != hex {
			writeOCI(w, digestInvalid("the upload does not have the digest "+d))
			return
		}
		if err := f.commitLink(r, c, t, hex, assets.Body{Spool: sp}); err != nil {
			fail(w, r, err)
			return
		}
		blobCreated(w, t, hex)
		return
	}
	s, err := f.h.Uploads.Start(r.Context(), c.P, c.Repo)
	if err != nil {
		fail(w, r, err)
		return
	}
	uploadHeaders(w, t, s.ID, 0)
	w.WriteHeader(http.StatusAccepted)
}

// mountable reports whether the blob digest of image from can be linked into the request's
// image: the caller reads the source repo and the source stores the link. Any miss is a plain
// upload start, never a refusal; a mount never contacts a remote.
func (f *Format) mountable(r *http.Request, c *route.Call, digest, from string) (string, bool) {
	hex, err := parseDigest(digest)
	if err != nil || from == "" {
		return "", false
	}
	src, err := parseName(from)
	if err != nil {
		return "", false
	}
	ctx := r.Context()
	repo := c.Repo
	if src.repo != c.Repo.Name {
		if repo, err = f.h.Repos.ByName(ctx, src.repo); err != nil || repo.Format != name {
			return "", false
		}
	}
	if ok, err := f.h.Access.Allows(ctx, r, c.P, repo, access.Read, perm.RepoDownload); err != nil || !ok {
		return "", false
	}
	a, err := f.h.Reader.Stored(ctx, r, c.P, repo, repopath.Path(linkPath(src.image, hex)))
	return hex, err == nil && a != nil
}

// spool reads the request body to a temp file under max_upload_size.
func (f *Format) spool(w http.ResponseWriter, r *http.Request) (*assets.Spooled, *ociError) {
	limit := f.h.Assets.MaxUploadSize()
	if r.ContentLength > limit {
		return nil, ociErr(http.StatusRequestEntityTooLarge, codeSizeInvalid, "the blob is larger than the max upload size of the server")
	}
	sp, err := f.h.Assets.Spool(http.MaxBytesReader(w, r.Body, limit), limit)
	if err != nil {
		var mbe *http.MaxBytesError
		switch {
		case errors.Is(err, assets.ErrTooLarge) || errors.As(err, &mbe):
			return nil, ociErr(http.StatusRequestEntityTooLarge, codeSizeInvalid, "the blob is larger than the max upload size of the server")
		case errors.Is(err, io.ErrUnexpectedEOF):
			return nil, ociErr(http.StatusBadRequest, codeBlobUploadInvalid, "the request body ended early")
		}
		return nil, ociErr(http.StatusInternalServerError, codeUnknown, "internal error")
	}
	return sp, nil
}

// contentRange reads a Content-Range of a chunk: "<from>-<to>", also with "bytes " and "/<size>".
// ok is false without the header.
func contentRange(r *http.Request) (from, to int64, ok bool, err *ociError) {
	v := strings.TrimSpace(r.Header.Get("Content-Range"))
	if v == "" {
		return 0, 0, false, nil
	}
	v = strings.TrimPrefix(v, "bytes ")
	v, _, _ = strings.Cut(v, "/")
	a, b, found := strings.Cut(v, "-")
	bad := ociErr(http.StatusBadRequest, codeBlobUploadInvalid, `Content-Range must be "<from>-<to>"`)
	if !found {
		return 0, 0, false, bad
	}
	from, e1 := strconv.ParseInt(a, 10, 64)
	to, e2 := strconv.ParseInt(b, 10, 64)
	if e1 != nil || e2 != nil || from < 0 || to < from {
		return 0, 0, false, bad
	}
	return from, to, true, nil
}

// appendChunk appends the body of r to upload id: at the offset of its Content-Range, else at the
// end. It returns the bytes received, or has answered.
func (f *Format) appendChunk(w http.ResponseWriter, r *http.Request, c *route.Call, t target, id string) (int64, bool) {
	offset := int64(-1)
	from, to, ok, oe := contentRange(r)
	if oe != nil {
		writeOCI(w, oe)
		return 0, false
	}
	var body io.Reader = r.Body
	if ok {
		if r.ContentLength >= 0 && r.ContentLength != to-from+1 {
			writeOCI(w, ociErr(http.StatusBadRequest, codeBlobUploadInvalid, "the Content-Length does not match the Content-Range"))
			return 0, false
		}
		offset, body = from, io.LimitReader(r.Body, to-from+1)
	}
	n, err := f.h.Uploads.Append(r.Context(), id, c.P, c.Repo, offset, body)
	if err != nil {
		f.uploadFailed(w, r, t, id, err)
		return 0, false
	}
	return n, true
}

// uploadFailed answers an error of an upload session.
func (f *Format) uploadFailed(w http.ResponseWriter, r *http.Request, t target, id string, err error) {
	var re *assets.RangeError
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &re):
		uploadHeaders(w, t, id, re.Received)
		w.Header().Del("Content-Length")
		writeOCI(w, ociErr(http.StatusRequestedRangeNotSatisfiable, codeBlobUploadInvalid, err.Error()))
	case errors.Is(err, assets.ErrUploadUnknown):
		writeOCI(w, ociErr(http.StatusNotFound, codeBlobUploadUnknown, "blob upload unknown"))
	case errors.Is(err, assets.ErrDigest):
		writeOCI(w, digestInvalid(err.Error()))
	case errors.Is(err, assets.ErrTooLarge), errors.As(err, &mbe):
		writeOCI(w, ociErr(http.StatusRequestEntityTooLarge, codeSizeInvalid, "the blob is larger than the max upload size of the server"))
	case errors.Is(err, io.ErrUnexpectedEOF):
		writeOCI(w, ociErr(http.StatusBadRequest, codeBlobUploadInvalid, "the request body ended early"))
	default:
		fail(w, r, err)
	}
}

// uploadID returns the id of the upload of t; it answers 404 for one that cannot exist.
func uploadID(w http.ResponseWriter, t target) (string, bool) {
	if !uploadRE.MatchString(t.ref) {
		writeOCI(w, ociErr(http.StatusNotFound, codeBlobUploadUnknown, "blob upload unknown"))
		return "", false
	}
	return t.ref, true
}

// patchUpload appends a chunk: 202 with the Range received.
func (f *Format) patchUpload(w http.ResponseWriter, r *http.Request, c *route.Call, t target) {
	id, ok := uploadID(w, t)
	if !ok {
		return
	}
	n, ok := f.appendChunk(w, r, c, t, id)
	if !ok {
		return
	}
	uploadHeaders(w, t, id, n)
	w.WriteHeader(http.StatusAccepted)
}

// uploadStatus answers GET of an upload: 204 with the Range received.
func (f *Format) uploadStatus(w http.ResponseWriter, r *http.Request, c *route.Call, t target) {
	id, ok := uploadID(w, t)
	if !ok {
		return
	}
	s, err := f.h.Uploads.Status(r.Context(), id, c.P, c.Repo)
	if err != nil {
		f.uploadFailed(w, r, t, id, err)
		return
	}
	uploadHeaders(w, t, id, s.Received)
	w.WriteHeader(http.StatusNoContent)
}

// finishUpload ends an upload with digest= and an optional last chunk, and stores the link: 201.
// A digest that the bytes do not have is 400 and the upload is gone.
func (f *Format) finishUpload(w http.ResponseWriter, r *http.Request, c *route.Call, t target) {
	id, ok := uploadID(w, t)
	if !ok {
		return
	}
	d := r.URL.Query().Get("digest")
	if d == "" {
		writeOCI(w, digestInvalid("the end of an upload needs digest="))
		return
	}
	hex, err := parseDigest(d)
	if err != nil {
		fail(w, r, err)
		return
	}
	if r.ContentLength != 0 {
		if _, ok := f.appendChunk(w, r, c, t, id); !ok {
			return
		}
	}
	sp, err := f.h.Uploads.Finish(r.Context(), id, c.P, c.Repo, hex, -1)
	if err != nil {
		f.uploadFailed(w, r, t, id, err)
		return
	}
	defer sp.Close()
	if err := f.commitLink(r, c, t, hex, assets.Body{Spool: sp}); err != nil {
		fail(w, r, err)
		return
	}
	blobCreated(w, t, hex)
}

// cancelUpload deletes an upload: 204.
func (f *Format) cancelUpload(w http.ResponseWriter, r *http.Request, c *route.Call, t target) {
	id, ok := uploadID(w, t)
	if !ok {
		return
	}
	if err := f.h.Uploads.Cancel(r.Context(), id, c.P, c.Repo); err != nil {
		f.uploadFailed(w, r, t, id, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// repoOfName returns the docker repo name of a full image name.
func repoOfName(full string) string {
	repo, _, _ := strings.Cut(full, "/")
	return repo
}
