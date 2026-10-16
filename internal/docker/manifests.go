package docker

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// unsupported is the answer to a method that a /v2/ path does not take.
var unsupported = ociErr(http.StatusMethodNotAllowed, codeUnsupported, "the registry does not take this method on this path")

// target parses the path of a /v2/ request of a repo and answers a path that names no image
// (404) or a bad name (400). The repo of the route policy is the first segment of the name.
func (f *Format) target(w http.ResponseWriter, r *http.Request) (target, bool) {
	w.Header().Set(apiVersionHeader, apiVersion)
	t, ok, err := parseTarget(r)
	switch {
	case err != nil:
		fail(w, r, err)
		return t, false
	case !ok:
		writeOCI(w, ociErr(http.StatusNotFound, codeNameUnknown, "the registry has no such path"))
		return t, false
	case t.image == "":
		_, host := f.hostOf(r)
		writeOCI(w, ociErr(http.StatusNotFound, codeNameUnknown, "name an image inside the repository: "+host+"/"+t.repo+"/<image>"))
		return t, false
	}
	return t, true
}

// get answers GET and HEAD of a manifest, a blob, an upload, the tags list or the referrers.
func (f *Format) get(w http.ResponseWriter, r *http.Request, c *route.Call) {
	t, ok := f.target(w, r)
	if !ok {
		return
	}
	switch {
	case t.action == actManifests:
		f.getManifest(w, r, c, t)
	case t.action == actBlobs:
		f.getBlob(w, r, c, t)
	case t.action == actUploads && t.ref != "":
		f.uploadStatus(w, r, c, t)
	case t.action == actTags:
		f.tagsList(w, r, c, t)
	case t.action == actReferrers:
		f.referrers(w, r, c, t)
	default:
		writeOCI(w, unsupported)
	}
}

// put answers PUT of a manifest and the end of an upload.
func (f *Format) put(w http.ResponseWriter, r *http.Request, c *route.Call) {
	t, ok := f.target(w, r)
	if !ok {
		return
	}
	switch {
	case t.action == actManifests:
		f.putManifest(w, r, c, t)
	case t.action == actUploads && t.ref != "":
		f.finishUpload(w, r, c, t)
	default:
		writeOCI(w, unsupported)
	}
}

// post answers POST of a new upload: monolithic, a mount, or the start of a session.
func (f *Format) post(w http.ResponseWriter, r *http.Request, c *route.Call) {
	t, ok := f.target(w, r)
	if !ok {
		return
	}
	if t.action != actUploads || t.ref != "" {
		writeOCI(w, unsupported)
		return
	}
	f.startUpload(w, r, c, t)
}

// patch answers PATCH of an upload: a chunk.
func (f *Format) patch(w http.ResponseWriter, r *http.Request, c *route.Call) {
	t, ok := f.target(w, r)
	if !ok {
		return
	}
	if t.action != actUploads || t.ref == "" {
		writeOCI(w, unsupported)
		return
	}
	f.patchUpload(w, r, c, t)
}

// delete answers DELETE of a manifest (by digest or by tag) and of an upload. A blob is not
// deleted on its own: the server removes the layers that no manifest uses.
func (f *Format) delete(w http.ResponseWriter, r *http.Request, c *route.Call) {
	t, ok := f.target(w, r)
	if !ok {
		return
	}
	switch {
	case t.action == actManifests:
		f.deleteManifest(w, r, c, t)
	case t.action == actUploads && t.ref != "":
		f.cancelUpload(w, r, c, t)
	case t.action == actBlobs:
		writeOCI(w, ociErr(http.StatusMethodNotAllowed, codeUnsupported, "delete the manifests; the server removes layers that no manifest uses"))
	default:
		writeOCI(w, unsupported)
	}
}

// refPath returns the asset path of a manifest reference: a digest or a tag. It serves a read
// and a delete, so a reference that cannot be a tag names no manifest (404).
func refPath(t target) (string, error) {
	if isDigest(t.ref) {
		hex, err := parseDigest(t.ref)
		if err != nil {
			return "", err
		}
		return manifestPath(t.image, hex), nil
	}
	if !tagRE.MatchString(t.ref) {
		return "", ociErr(http.StatusNotFound, codeManifestUnknown, "manifest unknown: "+t.name+" "+t.ref)
	}
	return tagPath(t.image, t.ref), nil
}

// getManifest serves a manifest by tag or digest with its stored bytes and media type, whatever
// the Accept header says.
func (f *Format) getManifest(w http.ResponseWriter, r *http.Request, c *route.Call, t target) {
	p, err := refPath(t)
	if err != nil {
		fail(w, r, err)
		return
	}
	a, err := f.h.Reader.Resolve(r.Context(), r, c.P, c.Repo, repopath.Path(p))
	switch {
	case errors.Is(err, assets.ErrRemoteFailed):
		writeOCI(w, ociErr(http.StatusBadGateway, codeUnknown, "The remote repository did not answer."))
		return
	case err != nil:
		fail(w, r, err)
		return
	case a == nil:
		writeOCI(w, ociErr(http.StatusNotFound, codeManifestUnknown, "manifest unknown: "+t.name+" "+t.ref))
		return
	}
	w.Header().Set("Docker-Content-Digest", "sha256:"+a.SHA256)
	f.h.Reader.ServeAsset(w, r, a, assets.ServeOptions{NoRedirect: true})
}

// putManifest stores a manifest, and its tag when the reference is a tag, in one commit. Every
// blob and child manifest it references must be in the image; a blob that another image of the
// repo links gets a link in the same commit.
func (f *Format) putManifest(w http.ResponseWriter, r *http.Request, c *route.Call, t target) {
	ctx := r.Context()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxManifest))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeOCI(w, ociErr(http.StatusRequestEntityTooLarge, codeSizeInvalid, "the manifest is larger than 4 MiB"))
			return
		}
		fail(w, r, err)
		return
	}
	m, err := ParseManifest(r.Header.Get("Content-Type"), body)
	if err != nil {
		fail(w, r, err)
		return
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	tag := ""
	if isDigest(t.ref) {
		want, err := parseDigest(t.ref)
		if err != nil {
			fail(w, r, err)
			return
		}
		if want != digest {
			writeOCI(w, digestInvalid("the manifest does not have the digest "+t.ref))
			return
		}
	} else {
		if !tagRE.MatchString(t.ref) {
			writeOCI(w, ociErr(http.StatusBadRequest, codeTagInvalid, "invalid tag: "+t.ref))
			return
		}
		tag = t.ref
	}
	origin := format.OriginOf(c.P.For(c.Repo.Name), assets.SourcePut)
	mp := manifestPath(t.image, digest)
	batch := map[string]bool{mp: true}
	var ws []assets.Write
	var locks []string
	for _, ch := range m.Children() {
		if ch.kind == "manifest" {
			locks = append(locks, manifestPath(t.image, ch.hex))
			continue
		}
		lp := linkPath(t.image, ch.hex)
		locks = append(locks, lp)
		ok, err := f.exists(ctx, c.Repo, lp, batch)
		if err != nil {
			fail(w, r, err)
			return
		}
		if ok {
			continue
		}
		_, err = f.q.RepoLinkOf(ctx, dbq.RepoLinkOfParams{RepoID: c.Repo.ID, Digest: ch.hex})
		if errors.Is(err, sql.ErrNoRows) {
			writeOCI(w, blobUnknownIn(ch.hex))
			return
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		batch[lp] = true
		ws = append(ws, assets.Write{Repo: c.Repo, Path: repopath.Path(lp), Body: assets.Body{Blob: ch.hex}, Origin: origin,
			Meta: &writeMeta{kind: kindLink, batch: batch}})
	}
	ws = append(ws, assets.Write{Repo: c.Repo, Path: repopath.Path(mp), Body: assets.Body{Spool: blobs.Bytes(body)},
		ContentType: m.MediaType, Origin: origin, Locks: locks, Meta: &writeMeta{kind: kindManifest, manifest: m, batch: batch}})
	if tag != "" {
		tp := tagPath(t.image, tag)
		batch[tp] = true
		ws = append(ws, assets.Write{Repo: c.Repo, Path: repopath.Path(tp), Body: assets.Body{Blob: digest},
			ContentType: m.MediaType, Origin: origin, Meta: &writeMeta{kind: kindTag, batch: batch}})
	}
	if _, err := f.h.Assets.CommitAll(ctx, ws); err != nil {
		fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Location", location(t.name, actManifests, "sha256:"+digest))
	h.Set("Docker-Content-Digest", "sha256:"+digest)
	if m.Subject != nil {
		h.Set("OCI-Subject", m.Subject.Digest)
	}
	w.WriteHeader(http.StatusCreated)
}

// deleteManifest deletes a manifest by digest (its tags follow) or a tag only.
func (f *Format) deleteManifest(w http.ResponseWriter, r *http.Request, c *route.Call, t target) {
	p, err := refPath(t)
	if err != nil {
		fail(w, r, err)
		return
	}
	if _, err := f.h.Assets.Get(r.Context(), c.Repo.ID, p); errors.Is(err, assets.ErrNotFound) {
		writeOCI(w, ociErr(http.StatusNotFound, codeManifestUnknown, "manifest unknown: "+t.name+" "+t.ref))
		return
	} else if err != nil {
		fail(w, r, err)
		return
	}
	if _, err := f.h.Assets.Remove(r.Context(), assets.Removal{Repo: c.Repo, Paths: []repopath.Path{repopath.Path(p)},
		Origin: format.OriginOf(c.P.For(c.Repo.Name), assets.SourcePut)}); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
