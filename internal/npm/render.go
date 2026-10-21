package npm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(v.Manifest), &m); err != nil {
			return nil, err
		}
		m["dist"], _ = json.Marshal(map[string]string{"integrity": v.Integrity, "shasum": v.Shasum, "tarball": urlBase + "/" + v.Tarball})
		delete(m, "deprecated")
		if v.Deprecated != "" {
			m["deprecated"], _ = json.Marshal(v.Deprecated)
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
		var dist map[string]json.RawMessage
		var tarball string
		if json.Unmarshal(m["dist"], &dist) != nil || json.Unmarshal(dist["tarball"], &tarball) != nil {
			delete(versions, v)
			continue
		}
		u, err := url.Parse(tarball)
		file := ""
		if err == nil {
			file = path.Base(u.Path)
		}
		if _, perr := repopath.Parse(name + "/-/" + file); err != nil || perr != nil || !strings.HasSuffix(file, ".tgz") {
			delete(versions, v)
			continue
		}
		dist["tarball"], _ = json.Marshal(urlBase + "/" + name + "/-/" + file)
		delete(dist, "attestations")
		m["dist"], _ = json.Marshal(dist)
	}
	if versions != nil {
		doc["versions"], _ = json.Marshal(versions)
	}
	return json.Marshal(doc)
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

// document renders the packument of s for urlBase, through the document cache.
func (h *Handler) document(ctx context.Context, s *source, name, urlBase string, abbreviated bool) ([]byte, error) {
	d, err := h.docs.Render(h.documentOf(ctx, s, name, urlBase, abbreviated))
	return d.Body, err
}

// distTagsOf returns the dist-tags of the source s.
func (h *Handler) distTagsOf(ctx context.Context, s *source, name string) (map[string]string, error) {
	if s.hosted != nil {
		if err := s.hosted.load(ctx, h.q); err != nil {
			return nil, err
		}
		return s.hosted.distTags(), nil
	}
	body, err := h.document(ctx, s, name, "", true)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Tags map[string]string `json:"dist-tags"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	if doc.Tags == nil {
		doc.Tags = map[string]string{}
	}
	return doc.Tags, nil
}
