package docker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// The Nexus import of docker repos. A Nexus docker repo lists its blobs (v2/-/blobs/sha256:<hex>)
// and its manifests by tag (v2/<image>/manifests/<tag>) and by digest
// (v2/<image>/manifests/sha256:<hex>). The import ignores the blobs: each manifest brings the
// blobs and the child manifests it names, fetched through the Docker v2 API of the Nexus repo,
// and commits them with the manifest (and its tag) in one commit. So the order of the source's
// pages does not matter.

// importer is the Importer of the format.
func (f *Format) importer() *format.Importer {
	return &format.Importer{Sources: []string{"docker"}, Target: f.importTarget, Store: f.importStore,
		Header: http.Header{"Accept": {manifestAccept + ", */*;q=0.1"}}}
}

// importRef splits a Nexus docker asset path v2/<image>/manifests/<ref> into the image and the
// reference; ok is false for any other path (a blob).
func importRef(p string) (image, ref string, ok bool) {
	rest, found := strings.CutPrefix(p, "v2/")
	if !found {
		return "", "", false
	}
	i := strings.LastIndex(rest, "/manifests/")
	if i <= 0 {
		return "", "", false
	}
	return rest[:i], rest[i+len("/manifests/"):], true
}

// importTarget maps a Nexus asset to its path: a manifest by tag to the tag path, a manifest by
// digest to the manifest path. Blobs are ignored. An image or tag name that OCI does not allow
// fails the asset.
func (f *Format) importTarget(_ context.Context, repo *repos.Repo, it format.ImportItem) (repopath.Path, bool, error) {
	image, ref, ok := importRef(it.Path)
	if !ok {
		return "", false, nil
	}
	var t target
	if err := t.setName(append([]string{repo.Name}, strings.Split(image, "/")...)); err != nil || t.image == "" {
		return "", false, fmt.Errorf("the image name %s is not a valid OCI name", image)
	}
	if isDigest(ref) {
		hex, err := parseDigest(ref)
		if err != nil {
			return "", false, err
		}
		return repopath.Path(manifestPath(image, hex)), true, nil
	}
	if !tagRE.MatchString(ref) {
		return "", false, fmt.Errorf("the tag %s is not a valid OCI tag", ref)
	}
	return repopath.Path(tagPath(image, ref)), true, nil
}

// importRun is the commit of one imported manifest: its writes and their spools.
type importRun struct {
	f      *Format
	ctx    context.Context
	repo   *repos.Repo
	it     format.ImportItem
	image  string
	origin assets.Origin
	batch  map[string]bool
	writes []assets.Write
	spools []*assets.Spooled
}

// importStore stores a downloaded manifest: it fetches every blob and child manifest that the
// image lacks, then commits the links, the child manifests, the manifest and the tag of a tag
// asset in one commit with the source import.
func (f *Format) importStore(ctx context.Context, repo *repos.Repo, it format.ImportItem, sp *assets.Spooled) (*assets.Result, error) {
	p, ok, err := f.importTarget(ctx, repo, it)
	if err != nil || !ok {
		return nil, err
	}
	op, _ := parsePath(string(p))
	body, err := readAllSpool(sp)
	if err != nil {
		return nil, err
	}
	run := &importRun{f: f, ctx: ctx, repo: repo, it: it, image: op.image, origin: it.Origin(), batch: map[string]bool{}}
	defer run.close()
	digest := sp.SHA256
	mp := manifestPath(op.image, digest)
	if op.kind == kindManifest && op.ref != digest {
		return nil, fmt.Errorf("the manifest does not have the digest sha256:%s", op.ref)
	}
	if err := run.manifest(digest, it.ContentType, body, assets.Body{Spool: sp}, 0); err != nil {
		return nil, err
	}
	target := mp
	if op.kind == kindTag {
		target = string(p)
		m, err := ParseManifest(it.ContentType, body)
		if err != nil {
			return nil, err
		}
		run.batch[target] = true
		run.writes = append(run.writes, assets.Write{Repo: repo, Path: p, Body: assets.Body{Blob: digest}, ContentType: m.MediaType,
			Origin: run.origin, Meta: &writeMeta{kind: kindTag, batch: run.batch}})
	}
	rs, err := f.h.Assets.CommitAll(ctx, run.writes)
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if string(r.Write.Path) == target {
			return r, nil
		}
	}
	return nil, errors.New("the import did not write its target")
}

// readAllSpool reads a spooled manifest (at most 4 MiB).
func readAllSpool(sp *assets.Spooled) ([]byte, error) {
	if sp.Size > maxManifest {
		return nil, errors.New("the manifest is larger than 4 MiB")
	}
	r, err := sp.Reader()
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(r, maxManifest+1))
}

// manifest adds the write of the manifest digest (body, sent with contentType) and, first, the
// writes of every blob and child manifest it names that the image lacks. depth bounds the
// nesting of indexes.
func (run *importRun) manifest(digest, contentType string, body []byte, b assets.Body, depth int) error {
	mp := manifestPath(run.image, digest)
	if run.batch[mp] {
		return nil
	}
	if depth > 4 {
		return errors.New("the index nests deeper than 4 levels")
	}
	m, err := ParseManifest(contentType, body)
	if err != nil {
		return err
	}
	children := m.Children()
	if err := run.blobs(children); err != nil {
		return err
	}
	for _, c := range children {
		if c.kind == "manifest" {
			if err := run.childManifest(c.hex, depth); err != nil {
				return err
			}
		}
	}
	run.batch[mp] = true
	run.writes = append(run.writes, assets.Write{Repo: run.repo, Path: repopath.Path(mp), Body: b, ContentType: m.MediaType,
		Origin: run.origin, Meta: &writeMeta{kind: kindManifest, manifest: m, batch: run.batch}})
	return nil
}

// has reports whether the image has path p already.
func (run *importRun) has(p string) (bool, error) {
	return run.f.exists(run.ctx, run.repo, p, run.batch)
}

// childManifest adds the writes of a child manifest of an index that the image lacks.
func (run *importRun) childManifest(hex string, depth int) error {
	if ok, err := run.has(manifestPath(run.image, hex)); err != nil || ok {
		return err
	}
	rc, ct, err := run.it.Fetch(run.ctx, "v2/"+run.image+"/manifests/sha256:"+hex, nil)
	if err != nil {
		return fmt.Errorf("fetch the manifest sha256:%s: %w", hex, err)
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, maxManifest+1))
	if err != nil {
		return fmt.Errorf("fetch the manifest sha256:%s: %w", hex, err)
	}
	if len(body) > maxManifest {
		return fmt.Errorf("the manifest sha256:%s is larger than 4 MiB", hex)
	}
	sp := blobs.Bytes(body)
	if sp.SHA256 != hex {
		return fmt.Errorf("the source sent other bytes for the manifest sha256:%s", hex)
	}
	return run.manifest(hex, ct, body, assets.Body{Spool: sp}, depth+1)
}

// blobFetches is the number of blobs of one manifest that download from the source at once.
const blobFetches = 6

// blobs adds, in the order of cs, the writes of the links that the image lacks of the blob
// children of cs: from a blob that another image of the repo links, else fetched from the source
// under max_upload_size, blobFetches at a time.
func (run *importRun) blobs(cs []child) error {
	type link struct {
		hex, path string
		fetch     bool
		sp        *assets.Spooled
	}
	var links []*link
	for _, c := range cs {
		if c.kind == "manifest" {
			continue
		}
		lp := linkPath(run.image, c.hex)
		if ok, err := run.has(lp); err != nil {
			return err
		} else if ok {
			continue
		}
		l := &link{hex: c.hex, path: lp}
		if _, err := run.f.q.RepoLinkOf(run.ctx, dbq.RepoLinkOfParams{RepoID: run.repo.ID, Digest: c.hex}); errors.Is(err, sql.ErrNoRows) {
			l.fetch = true
		} else if err != nil {
			return err
		}
		links = append(links, l)
	}

	// The first failed fetch stops the others.
	ctx, cancel := context.WithCancel(run.ctx)
	defer cancel()
	var mu sync.Mutex
	var first error
	var wg sync.WaitGroup
	slots := make(chan struct{}, blobFetches)
	for _, l := range links {
		if !l.fetch {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			sp, err := run.fetchBlob(ctx, l.hex)
			mu.Lock()
			defer mu.Unlock()
			l.sp = sp
			if err != nil && first == nil {
				first = err
				cancel()
			}
		}()
	}
	wg.Wait()
	for _, l := range links {
		if l.sp != nil {
			run.spools = append(run.spools, l.sp)
		}
	}
	if first != nil {
		return first
	}
	if err := run.ctx.Err(); err != nil {
		return err
	}
	for _, l := range links {
		body := assets.Body{Blob: l.hex}
		if l.fetch {
			body = assets.Body{Spool: l.sp}
		}
		run.batch[l.path] = true
		run.writes = append(run.writes, assets.Write{Repo: run.repo, Path: repopath.Path(l.path), Body: body,
			Origin: run.origin, Meta: &writeMeta{kind: kindLink, batch: run.batch}})
	}
	return nil
}

// fetchBlob downloads blob hex of the image from the source and spools it under max_upload_size;
// the spool holds bytes with that digest.
func (run *importRun) fetchBlob(ctx context.Context, hex string) (*assets.Spooled, error) {
	rc, _, err := run.it.Fetch(ctx, "v2/"+run.image+"/blobs/sha256:"+hex, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch the blob sha256:%s: %w", hex, err)
	}
	limit := run.f.h.Assets.MaxUploadSize()
	sp, err := run.f.h.Assets.Spool(rc, limit)
	rc.Close()
	if errors.Is(err, assets.ErrTooLarge) {
		return nil, fmt.Errorf("the blob sha256:%s is larger than the upload limit of %d bytes", hex, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("fetch the blob sha256:%s: %w", hex, err)
	}
	if sp.SHA256 != hex {
		sp.Close()
		return nil, fmt.Errorf("the source sent other bytes for the blob sha256:%s", hex)
	}
	return sp, nil
}

// close removes the spools of the fetched blobs.
func (run *importRun) close() {
	for _, sp := range run.spools {
		sp.Close()
	}
}
