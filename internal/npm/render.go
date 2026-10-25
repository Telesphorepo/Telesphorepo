package npm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/npm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// abbreviatedType is the content type of the abbreviated packument (npm's install metadata).
const abbreviatedType = "application/vnd.npm.install-v1+json"

// abbreviatedFields are the fields of a version that the abbreviated packument keeps (npm's
// package-metadata.md).
var abbreviatedFields = []string{"name", "version", "deprecated", "dependencies", "optionalDependencies", "devDependencies",
	"bundleDependencies", "bundledDependencies", "peerDependencies", "peerDependenciesMeta", "acceptDependencies", "bin",
	"directories", "dist", "engines", "_hasShrinkwrap", "hasInstallScript", "cpu", "os", "libc", "funding"}

// topFields are the fields of the full packument that come from the latest version.
var topFields = []string{"description", "readme", "homepage", "keywords", "repository", "author", "license", "bugs"}

// wantsAbbreviated reports whether the request asks for the abbreviated packument.
func wantsAbbreviated(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), abbreviatedType)
}

// errNoPackage is a name that the repo does not have.
var errNoPackage = errors.New("no such package")

// hosted is the rows of a package of a hosted repo: the package row, and the versions and tags
// once load has read them.
type hosted struct {
	repoID   int64
	name     string
	pkg      dbq.GetPackageRow
	versions []dbq.ListVersionsRow
	tags     []dbq.ListTagsRow
}

// packageOf reads the package row of name of repo; errNoPackage when it has none.
func packageOf(ctx context.Context, q *dbq.Queries, repoID int64, name string) (*hosted, error) {
	p, err := q.GetPackage(ctx, dbq.GetPackageParams{RepoID: repoID, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoPackage
	}
	if err != nil {
		return nil, err
	}
	return &hosted{repoID: repoID, name: name, pkg: p}, nil
}

// load reads the versions and the tags of the package.
func (d *hosted) load(ctx context.Context, q *dbq.Queries) error {
	var err error
	if d.versions, err = q.ListVersions(ctx, dbq.ListVersionsParams{RepoID: d.repoID, Name: d.name, Limit: maxVersions}); err != nil {
		return err
	}
	d.tags, err = q.ListTags(ctx, dbq.ListTagsParams{RepoID: d.repoID, Name: d.name})
	return err
}

func (d *hosted) distTags() map[string]string {
	out := map[string]string{}
	for _, t := range d.tags {
		out[t.Tag] = t.Version
	}
	return out
}

func rfc3339(unix int64) string { return time.Unix(unix, 0).UTC().Format("2006-01-02T15:04:05.000Z") }

// render builds the packument of a hosted package: tarball URLs under urlBase (the registry URL
// without a trailing slash).
func (d *hosted) render(name, urlBase string, abbreviated bool) ([]byte, error) {
	versions := map[string]map[string]json.RawMessage{}
	times := map[string]string{"created": rfc3339(d.pkg.Created), "modified": rfc3339(d.pkg.Modified)}
	var latest map[string]json.RawMessage
	tags := d.distTags()
	for _, v := range d.versions {
		m, err := storedManifest(dbq.GetVersionRow{Tarball: v.Tarball, Integrity: v.Integrity, Shasum: v.Shasum,
			Deprecated: v.Deprecated, Manifest: v.Manifest}, urlBase)
		if err != nil {
			return nil, err
		}
		if v.Version == tags["latest"] {
			latest = m
		}
		if abbreviated {
			m = pick(m, abbreviatedFields)
		}
		versions[v.Version] = m
		times[v.Version] = rfc3339(v.PublishedAt)
	}
	if abbreviated {
		return json.Marshal(map[string]any{"name": name, "modified": times["modified"], "dist-tags": tags, "versions": versions})
	}
	doc := map[string]any{"_id": name, "_rev": d.pkg.Rev, "name": name, "dist-tags": tags, "versions": versions, "time": times}
	for _, f := range topFields {
		if v, ok := latest[f]; ok {
			doc[f] = v
		}
	}
	return json.Marshal(doc)
}

// storedManifest is the manifest of a stored version as the full packument lists it: dist with the
// tarball URL under urlBase, and the deprecation of the row.
func storedManifest(v dbq.GetVersionRow, urlBase string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(v.Manifest), &m); err != nil {
		return nil, err
	}
	m["dist"], _ = json.Marshal(map[string]string{"integrity": v.Integrity, "shasum": v.Shasum, "tarball": urlBase + "/" + v.Tarball})
	delete(m, "deprecated")
	if v.Deprecated != "" {
		m["deprecated"], _ = json.Marshal(v.Deprecated)
	}
	return m, nil
}

// errNoVersion is a version or dist-tag that the package does not have.
var errNoVersion = errors.New("no such version")

// version renders the manifest of version or dist-tag ref of the package from its two rows; a
// dist-tag wins over a version of the same name.
func (d *hosted) version(ctx context.Context, q *dbq.Queries, ref, urlBase string) ([]byte, error) {
	v := ref
	switch tv, err := q.GetTag(ctx, dbq.GetTagParams{RepoID: d.repoID, Name: d.name, Tag: ref}); {
	case err == nil:
		v = tv
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	row, err := q.GetVersion(ctx, dbq.GetVersionParams{RepoID: d.repoID, Name: d.name, Version: v})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoVersion
	}
	if err != nil {
		return nil, err
	}
	m, err := storedManifest(row, urlBase)
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// pick returns the fields of m that keep names.
func pick(m map[string]json.RawMessage, keep []string) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(keep))
	for _, k := range keep {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

// rewriteRemote turns a remote packument into this repo's: each version's dist.tarball points
// under urlBase at <name>/-/<file> (the file name of the remote URL), and dist.attestations goes.
// integrity, shasum and signatures stay. A version whose tarball URL has no .tgz file name goes.
func rewriteRemote(body []byte, name, urlBase string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil || doc == nil {
		return nil, errors.New("the remote packument is not a JSON object")
	}
	var versions map[string]map[string]json.RawMessage
	if raw, ok := doc["versions"]; ok {
		if err := json.Unmarshal(raw, &versions); err != nil {
			return nil, errors.New("the remote packument has no valid versions")
		}
	}
	for v, m := range versions {
		if !rewriteVersion(m, name, urlBase) {
			delete(versions, v)
		}
	}
	if versions != nil {
		doc["versions"], _ = json.Marshal(versions)
	}
	return json.Marshal(doc)
}

// rewriteVersion turns the manifest m of a version of a remote packument into this repo's, as
// rewriteRemote does; false for a version whose tarball URL has no .tgz file name.
func rewriteVersion(m map[string]json.RawMessage, name, urlBase string) bool {
	var dist map[string]json.RawMessage
	var tarball string
	if json.Unmarshal(m["dist"], &dist) != nil || json.Unmarshal(dist["tarball"], &tarball) != nil {
		return false
	}
	file, ok := remoteFile(name, tarball)
	if !ok {
		return false
	}
	dist["tarball"], _ = json.Marshal(urlBase + "/" + name + "/-/" + file)
	delete(dist, "attestations")
	m["dist"], _ = json.Marshal(dist)
	return true
}

// maxKey is the longest key of a remote packument that a scan reads.
const maxKey = 64 << 10

// scanDoc reads the packument that open opens as a stream and calls fn with each top-level key;
// fn reads its value (raw or skip) and returns stop=true to end the read there.
func scanDoc(open func() (io.ReadCloser, error), fn func(s *scanner, key string) (bool, error)) error {
	rc, err := open()
	if err != nil {
		return err
	}
	defer rc.Close()
	s := newScanner(rc)
	err = s.members(maxKey, func(key string) (bool, error) { return fn(s, key) })
	if errors.Is(err, errSyntax) {
		return errors.New("the remote packument is not valid JSON")
	}
	return err
}

// remoteManifest renders the manifest of version or dist-tag ref of package name from the full
// remote packument that open opens (values up to limit bytes), reading the document only up to
// that version. A dist-tag wins over a version of the same name; npm refuses a dist-tag that is a
// SemVer version, so a ref that is one is read without the tags. When the versions come before
// the tags and ref is a tag, a second read finds its version.
func remoteManifest(open func() (io.ReadCloser, error), limit int, name, ref, urlBase string) ([]byte, error) {
	want := "" // the version to read; "" until the tags are read
	if _, err := version.ParseSemVer(ref); err == nil {
		want = ref
	}
	var found, cand json.RawMessage                         // the manifest of want; of ref, read before the tags
	tagsRead, versionsRead, tagsLate := false, false, false // tagsLate: the tags came after the versions
	readVersions := func(s *scanner) error {
		versionsRead = true
		if c, err := s.peek(); err != nil || c != '{' {
			return s.skip()
		}
		return s.members(maxKey, func(k string) (bool, error) {
			var err error
			switch {
			case want != "" && k == want:
				found, err = s.raw(limit)
				return true, err
			case want == "" && k == ref:
				cand, err = s.raw(limit)
				return false, err
			}
			return false, s.skip()
		})
	}
	err := scanDoc(open, func(s *scanner, key string) (bool, error) {
		switch key {
		case "dist-tags":
			raw, err := s.raw(limit)
			if err != nil {
				return false, err
			}
			var tags map[string]string
			if err := json.Unmarshal(raw, &tags); err != nil {
				return false, errors.New("the remote packument has invalid dist-tags")
			}
			tagsRead, tagsLate = true, versionsRead
			if want == "" {
				want = ref
				if tv, ok := tags[ref]; ok {
					want = tv
				}
				if versionsRead && want == ref {
					found = cand
				}
			}
			return versionsRead, nil
		case "versions":
			if err := readVersions(s); err != nil {
				return false, err
			}
			// A found version ends the read inside the versions; else, with want known, the
			// version is missing.
			return found != nil || want != "", nil
		}
		return false, s.skip()
	})
	if err != nil {
		return nil, err
	}
	switch {
	case !tagsRead && want == "":
		found = cand // no dist-tags: ref names a version
	case found == nil && tagsLate && want != ref:
		// The versions came before the tags: read them again for the version of the tag.
		err = scanDoc(open, func(s *scanner, key string) (bool, error) {
			if key != "versions" {
				return false, s.skip()
			}
			return true, readVersions(s)
		})
		if err != nil {
			return nil, err
		}
	}
	var m map[string]json.RawMessage
	if found == nil || json.Unmarshal(found, &m) != nil || m == nil || !rewriteVersion(m, name, urlBase) {
		return nil, errNoVersion
	}
	return json.Marshal(m)
}

// remoteFile returns the file name of the remote tarball URL of a version of name, the file of
// <name>/-/<file> in a proxy; ok is false for a URL without a .tgz file name.
func remoteFile(name, tarball string) (string, bool) {
	u, err := url.Parse(tarball)
	if err != nil {
		return "", false
	}
	file := path.Base(u.Path)
	if _, err := repopath.Parse(name + "/-/" + file); err != nil || !strings.HasSuffix(file, ".tgz") {
		return "", false
	}
	return file, true
}

// source is where the packument of a name comes from: a hosted repo's rows, or the cached remote
// document of a proxy.
type source struct {
	repo   *repos.Repo
	hosted *hosted
	remote *assets.Asset
}

// sourceOf returns the source of name in repo (a hosted repo or a proxy) for form; errNoPackage
// when repo does not have the name. A proxy fetches the remote document when its copy is stale;
// assets.ErrRemoteFailed when the remote fails with nothing cached.
func (h *Handler) sourceOf(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, name string, abbreviated bool) (*source, error) {
	switch repo.Kind {
	case repos.KindHosted:
		d, err := packageOf(ctx, h.q, repo.ID, name)
		if err != nil {
			return nil, err
		}
		return &source{repo: repo, hosted: d}, nil
	case repos.KindProxy:
		form := formFull
		if abbreviated {
			form = formAbbrev
		}
		pp, err := repopath.Parse(packumentPath(form, name))
		if err != nil {
			return nil, errNoPackage
		}
		a, err := h.host.Reader.Resolve(ctx, r, p, repo, pp)
		if err != nil {
			return nil, err
		}
		if a == nil {
			return nil, errNoPackage
		}
		return &source{repo: repo, remote: a}, nil
	}
	return nil, errNoPackage
}

// resolve returns the source of name in repo c (a group: the member that owns the name).
func (h *Handler) resolve(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, name string, abbreviated bool) (*source, error) {
	if repo.Kind != repos.KindGroup {
		return h.sourceOf(ctx, r, p, repo, name, abbreviated)
	}
	var found *source
	owner, err := h.host.Reader.Owner(ctx, r, p, repo, func(ctx context.Context, m *repos.Repo) (bool, error) {
		s, err := h.sourceOf(ctx, r, p, m, name, abbreviated)
		if errors.Is(err, errNoPackage) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		found = s
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, errNoPackage
	}
	return found, nil
}

// documentOf returns the cache key of the packument of s for urlBase and the build of its body:
// a hosted package's key holds its revision, a proxy's the SHA-256 of the cached remote
// packument.
func (h *Handler) documentOf(ctx context.Context, s *source, name, urlBase string, abbreviated bool) (string, func() ([]byte, error)) {
	form := formFull
	if abbreviated {
		form = formAbbrev
	}
	if s.hosted != nil {
		key := format.DocumentKey("hosted", form, name, s.hosted.pkg.Rev, urlBase, s.repo.Name)
		return key, func() ([]byte, error) {
			if err := s.hosted.load(ctx, h.q); err != nil {
				return nil, err
			}
			return s.hosted.render(name, urlBase, abbreviated)
		}
	}
	key := format.DocumentKey("remote", form, name, s.remote.SHA256, urlBase)
	return key, func() ([]byte, error) {
		body, err := h.assets.ReadAll(ctx, s.remote, h.assets.MaxUploadSize())
		if err != nil {
			return nil, err
		}
		return rewriteRemote(body, name, urlBase)
	}
}

// versionOf returns the cache key of the manifest of version or dist-tag ref of s for urlBase and
// its build: a hosted package's key holds its revision, a proxy's the SHA-256 of the cached full
// remote packument. The build reads that one version (errNoVersion when s has none).
func (h *Handler) versionOf(ctx context.Context, s *source, name, ref, urlBase string) (string, func() ([]byte, error)) {
	if s.hosted != nil {
		key := format.DocumentKey("hosted-version", name, s.hosted.pkg.Rev, ref, urlBase, s.repo.Name)
		return key, func() ([]byte, error) { return s.hosted.version(ctx, h.q, ref, urlBase) }
	}
	key := format.DocumentKey("remote-version", name, s.remote.SHA256, ref, urlBase)
	open := func() (io.ReadCloser, error) { return h.assets.Open(ctx, s.remote) }
	return key, func() ([]byte, error) { return remoteManifest(open, int(h.assets.MaxUploadSize()), name, ref, urlBase) }
}

// distTagsOf returns the dist-tags of the source s: a hosted package's rows, or the dist-tags of
// the cached remote packument, read up to them.
func (h *Handler) distTagsOf(ctx context.Context, s *source) (map[string]string, error) {
	if s.hosted != nil {
		var err error
		if s.hosted.tags, err = h.q.ListTags(ctx, dbq.ListTagsParams{RepoID: s.hosted.repoID, Name: s.hosted.name}); err != nil {
			return nil, err
		}
		return s.hosted.distTags(), nil
	}
	tags := map[string]string{}
	open := func() (io.ReadCloser, error) { return h.assets.Open(ctx, s.remote) }
	err := scanDoc(open, func(sc *scanner, key string) (bool, error) {
		if key != "dist-tags" {
			return false, sc.skip()
		}
		raw, err := sc.raw(int(h.assets.MaxUploadSize()))
		if err != nil {
			return false, err
		}
		if json.Unmarshal(raw, &tags) != nil {
			return false, errors.New("the remote packument has invalid dist-tags")
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if tags == nil {
		tags = map[string]string{}
	}
	return tags, nil
}
