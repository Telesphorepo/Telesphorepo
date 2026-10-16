package docker

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// writeMeta is the Write.Meta of a write from a docker handler: the kind of the object, the parsed
// manifest of a manifest write, and the paths of every write of the same commit.
type writeMeta struct {
	kind     string
	manifest *Manifest
	batch    map[string]bool
}

// describe gives the content type and the kind of an OCI object path and refuses every other
// path: a link is an index file (not searched), a manifest is metadata, a tag is the artifact.
// The handlers set the content type of manifests and tags (Write.ContentType).
func describe(p repopath.Path) (assets.Describe, bool) {
	op, ok := parsePath(string(p))
	if !ok {
		return assets.Describe{}, false
	}
	d := assets.Describe{ContentType: assets.FallbackContentType}
	switch op.kind {
	case kindLink:
		d.Kind = assets.KindIndex
	case kindManifest:
		d.Kind = assets.KindMetadata
	default:
		d.Kind = assets.KindArtifact
	}
	return d, true
}

// admit checks a write of an OCI object: the generic upload and file routes cannot store one, a
// link and a manifest are stored at the path of their digest, a pushed or imported manifest has
// every blob and child manifest it references in the image or in the same commit, and a tag
// names a manifest of the image. The same bytes at a path are kept.
func (f *Format) admit(ctx context.Context, w *assets.Write, existing *assets.Asset) (assets.Decision, error) {
	d, err := f.admitObject(ctx, w, existing)
	var oe *ociError
	if err != nil && w.Origin.Source == assets.SourceProxy && errors.As(err, &oe) {
		// The proxy reports a refused remote file as a remote failure.
		return 0, assets.Refuse(oe.status, "%s", oe.message)
	}
	return d, err
}

// admitObject is admit without the mapping of a proxy's refusals.
func (f *Format) admitObject(ctx context.Context, w *assets.Write, existing *assets.Asset) (assets.Decision, error) {
	op, ok := parsePath(string(w.Path))
	if !ok {
		return 0, assets.Refuse(http.StatusBadRequest, "%s is not a path of a docker repository", w.Path)
	}
	meta, _ := w.Meta.(*writeMeta)
	src := w.Origin.Source
	if meta == nil && (src == assets.SourcePut || src == assets.SourceUI) {
		return 0, assets.Refuse(http.StatusBadRequest, "push with an OCI client (docker push, helm push, oras push)")
	}
	digest := w.Body.Digest()
	var batch map[string]bool
	if meta != nil {
		batch = meta.batch
	}
	switch op.kind {
	case kindLink:
		if op.ref != digest {
			return 0, digestInvalid("the blob does not have the digest sha256:" + op.ref)
		}
	case kindManifest:
		if op.ref != digest {
			return 0, digestInvalid("the manifest does not have the digest sha256:" + op.ref)
		}
		if src == assets.SourceProxy {
			// A proxy fetches the children of a manifest when a client asks for them.
			m, err := f.manifestOf(ctx, w)
			if err != nil {
				return 0, err
			}
			w.ContentType = m.MediaType
		}
		if src == assets.SourcePut || src == assets.SourceImport {
			m, err := f.manifestOf(ctx, w)
			if err != nil {
				return 0, err
			}
			for _, c := range m.Children() {
				p := linkPath(op.image, c.hex)
				if c.kind == "manifest" {
					p = manifestPath(op.image, c.hex)
				}
				ok, err := f.exists(ctx, w.Repo, p, batch)
				if err != nil {
					return 0, err
				}
				if !ok {
					return 0, blobUnknownIn(c.hex)
				}
			}
		}
	case kindTag:
		if src == assets.SourceProxy {
			// The proxy stores the manifest of a fetched tag after the tag (afterCommit).
			m, err := f.manifestOf(ctx, w)
			if err != nil {
				return 0, err
			}
			w.ContentType = m.MediaType
			break
		}
		ok, err := f.exists(ctx, w.Repo, manifestPath(op.image, digest), batch)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, ociErr(http.StatusBadRequest, codeManifestUnknown, "the tag names the manifest sha256:"+digest+", which the image does not have")
		}
	}
	if existing != nil && existing.SHA256 == digest {
		return assets.Keep, nil
	}
	return assets.Store, nil
}

// exists reports whether path is a write of the same commit (batch) or an asset of repo.
func (f *Format) exists(ctx context.Context, repo *repos.Repo, path string, batch map[string]bool) (bool, error) {
	if batch[path] {
		return true, nil
	}
	_, err := f.h.Assets.Get(ctx, repo.ID, path)
	if errors.Is(err, assets.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// manifestOf returns the manifest of a manifest write: the parsed one of the handler, else the
// bytes of the body (at most 4 MiB) parsed with the content type of the write.
func (f *Format) manifestOf(ctx context.Context, w *assets.Write) (*Manifest, error) {
	if meta, ok := w.Meta.(*writeMeta); ok && meta.manifest != nil {
		return meta.manifest, nil
	}
	var rd io.Reader
	if sp := w.Body.Spool; sp != nil {
		r, err := sp.Reader()
		if err != nil {
			return nil, err
		}
		rd = r
	} else {
		rc, _, err := f.h.Blobs.Open(ctx, w.Body.Blob)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		rd = rc
	}
	b, err := io.ReadAll(io.LimitReader(rd, maxManifest+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxManifest {
		return nil, ociErr(http.StatusRequestEntityTooLarge, codeSizeInvalid, "the manifest is larger than 4 MiB")
	}
	return ParseManifest(w.ContentType, b)
}

// onCommitted writes the rows of a stored object in the transaction of the write: a link with
// the uses that the manifests of the image make of it, a manifest with its references (each blob
// it uses counts one more use of its link), a tag with its digest.
func (f *Format) onCommitted(ctx context.Context, tx *sql.Tx, w *assets.Write, a *assets.Asset, _ *assets.Asset) error {
	op, ok := parsePath(a.Path)
	if !ok {
		return nil
	}
	q := dbq.New(tx)
	repoID, now := w.Repo.ID, time.Now().Unix()
	switch op.kind {
	case kindLink:
		refs, err := q.CountChildRefs(ctx, dbq.CountChildRefsParams{RepoID: repoID, Image: op.image, Child: op.ref})
		if err != nil {
			return err
		}
		var unref int64
		if refs == 0 {
			unref = now
		}
		return q.UpsertLink(ctx, dbq.UpsertLinkParams{RepoID: repoID, Image: op.image, Digest: op.ref, Size: a.Size, Refs: refs, UnreferencedAt: unref})
	case kindManifest:
		m, err := f.manifestOf(ctx, w)
		if err != nil {
			return err
		}
		subject := ""
		if m.Subject != nil {
			subject = m.Subject.hex
		}
		if err := q.InsertManifest(ctx, dbq.InsertManifestParams{RepoID: repoID, Image: op.image, Digest: op.ref, MediaType: m.MediaType,
			ArtifactType: m.artifactType(), ConfigType: m.configType(), Subject: subject, Size: a.Size, TotalSize: m.TotalSize(),
			Platforms: m.platforms(), Annotations: m.annotations(), CreatedAt: a.CreatedAt.Unix()}); err != nil {
			return err
		}
		for _, c := range m.Children() {
			if err := q.InsertRef(ctx, dbq.InsertRefParams{RepoID: repoID, Image: op.image, Manifest: op.ref, Child: c.hex, Kind: c.kind}); err != nil {
				return err
			}
			if c.kind == "blob" {
				if err := q.AddLinkRefs(ctx, dbq.AddLinkRefsParams{Delta: 1, Now: now, RepoID: repoID, Image: op.image, Digest: c.hex}); err != nil {
					return err
				}
			}
		}
		if err := q.UpsertImage(ctx, dbq.UpsertImageParams{RepoID: repoID, Image: op.image, FullName: w.Repo.Name + "/" + op.image}); err != nil {
			return err
		}
		return q.AddImageManifests(ctx, dbq.AddImageManifestsParams{Delta: 1, RepoID: repoID, Image: op.image})
	case kindTag:
		return q.UpsertTag(ctx, dbq.UpsertTagParams{RepoID: repoID, Image: op.image, Tag: op.ref, Digest: a.SHA256, UpdatedAt: a.UpdatedAt.Unix()})
	}
	return nil
}

// kindOrder removes tags first, then manifests, then links, so a delete of a whole image in one
// batch drops the uses of each link before the link.
var kindOrder = map[string]int{kindTag: 0, kindManifest: 1, kindLink: 2}

// onRemoved drops the rows of removed objects in the transaction of the remove. A manifest that
// an index outside the removal lists is refused (409), as is a link that a manifest outside the
// removal uses. The removal holds the files of this batch and every file under its prefixes: a
// folder delete removes the links of an image (_blobs/ sorts first) in an earlier batch than its
// manifests, and a child manifest in an earlier batch than its index. A removed manifest gives
// back one use of each blob it references (a link that is gone already changes no row), and its
// tags follow it.
func (f *Format) onRemoved(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*assets.Asset, prefixes []repopath.Path) ([]repopath.Path, error) {
	q := dbq.New(tx)
	now := time.Now().Unix()
	batch := make(map[string]bool, len(removed))
	type object struct {
		objectPath
		path string
	}
	var objs []object
	for _, a := range removed {
		batch[a.Path] = true
		if op, ok := parsePath(a.Path); ok {
			objs = append(objs, object{op, a.Path})
		}
	}
	slices.SortStableFunc(objs, func(a, b object) int { return kindOrder[a.kind] - kindOrder[b.kind] })
	var follow []repopath.Path
	for _, o := range objs {
		switch o.kind {
		case kindTag:
			if err := q.DeleteTag(ctx, dbq.DeleteTagParams{RepoID: repo.ID, Image: o.image, Tag: o.ref}); err != nil {
				return nil, err
			}
		case kindManifest:
			parents, err := q.ParentIndexes(ctx, dbq.ParentIndexesParams{RepoID: repo.ID, Image: o.image, Child: o.ref, Limit: int64(len(batch) + 1)})
			if err != nil {
				return nil, err
			}
			for _, p := range parents {
				if !batch[manifestPath(o.image, p)] && !manifestsGo(prefixes, o.image) {
					return nil, assets.Refuse(http.StatusConflict, "an index uses this manifest; delete the index first")
				}
			}
			refs, err := q.ListRefs(ctx, dbq.ListRefsParams{RepoID: repo.ID, Image: o.image, Manifest: o.ref})
			if err != nil {
				return nil, err
			}
			for _, c := range refs {
				if c.Kind != "blob" {
					continue
				}
				if err := q.AddLinkRefs(ctx, dbq.AddLinkRefsParams{Delta: -1, Now: now, RepoID: repo.ID, Image: o.image, Digest: c.Child}); err != nil {
					return nil, err
				}
			}
			if err := q.DeleteRefs(ctx, dbq.DeleteRefsParams{RepoID: repo.ID, Image: o.image, Manifest: o.ref}); err != nil {
				return nil, err
			}
			if err := q.DeleteManifest(ctx, dbq.DeleteManifestParams{RepoID: repo.ID, Image: o.image, Digest: o.ref}); err != nil {
				return nil, err
			}
			if err := q.AddImageManifests(ctx, dbq.AddImageManifestsParams{Delta: -1, RepoID: repo.ID, Image: o.image}); err != nil {
				return nil, err
			}
			if err := q.DeleteEmptyImage(ctx, dbq.DeleteEmptyImageParams{RepoID: repo.ID, Image: o.image}); err != nil {
				return nil, err
			}
			tags, err := q.TagsOfDigest(ctx, dbq.TagsOfDigestParams{RepoID: repo.ID, Image: o.image, Digest: o.ref})
			if err != nil {
				return nil, err
			}
			for _, t := range tags {
				if p := tagPath(o.image, t); !batch[p] {
					follow = append(follow, repopath.Path(p))
				}
			}
		case kindLink:
			l, err := q.GetLink(ctx, dbq.GetLinkParams{RepoID: repo.ID, Image: o.image, Digest: o.ref})
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if l.Refs > 0 && !manifestsGo(prefixes, o.image) {
				return nil, assets.Refuse(http.StatusConflict, "a manifest uses this layer")
			}
			if err := q.DeleteLink(ctx, dbq.DeleteLinkParams{RepoID: repo.ID, Image: o.image, Digest: o.ref}); err != nil {
				return nil, err
			}
		}
	}
	return follow, nil
}

// manifestsGo reports whether a folder of prefixes holds every manifest of image, so the removal
// takes each manifest that uses a link of the image or that an index of the image lists.
func manifestsGo(prefixes []repopath.Path, image string) bool {
	dir := manifestPath(image, "")
	for _, p := range prefixes {
		if strings.HasPrefix(dir, string(p)) {
			return true
		}
	}
	return false
}
