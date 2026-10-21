package npm

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// The versions of a package of a proxy (Versions, Builds and ArtifactUsage), read from the
// abbreviated remote packument through the read path: the form whose presence makes a proxy
// member the owner of a name in a group read (file and packument resolve with it, and npm
// install asks for it), so the Files tab of a group lists the member that serves the downloads.
// The abbreviated form has no time map: the dates come from the full form when the proxy has it
// stored already (a GET of a version manifest caches it), and are zero otherwise, so a page
// costs no second remote fetch.

// remoteVersion is one version that the remote packument names: its sort key and the file of
// its tarball, <name>/-/<file>.
type remoteVersion struct {
	version   string
	key       string
	file      string
	published time.Time
}

// remoteList is the parsed packument of a package of a proxy: its versions newest first and the
// version of its latest tag.
type remoteList struct {
	versions []remoteVersion
	latest   string
}

// find returns the version v, or ok=false.
func (l *remoteList) find(v string) (remoteVersion, bool) {
	for _, rv := range l.versions {
		if rv.version == v {
			return rv, true
		}
	}
	return remoteVersion{}, false
}

// maxLists is the number of parsed packuments that listCache keeps.
const maxLists = 64

// listCache keeps parsed remote packuments by the SHA-256s of the cached documents, so a page of
// versions parses a large packument once per change of the remote copy. When full, it drops an
// arbitrary entry.
type listCache struct {
	mu sync.Mutex
	m  map[string]*remoteList
}

func newListCache() *listCache { return &listCache{m: map[string]*remoteList{}} }

func (c *listCache) get(key string) *remoteList {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[key]
}

func (c *listCache) put(key string, l *remoteList) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if len(c.m) < maxLists {
			break
		}
		delete(c.m, k)
	}
	c.m[key] = l
}

// remoteVersions returns the versions of pkg in proxy repo, or nil when the proxy does not have
// the name. It reads the abbreviated packument as the group read path does (fetching it when the
// cached copy is stale or missing); assets.ErrRemoteFailed when the remote fails with nothing
// cached.
func (h *Handler) remoteVersions(ctx context.Context, repo *repos.Repo, pkg string) (*remoteList, error) {
	s, err := h.sourceOf(ctx, nil, nil, repo, pkg, true)
	if errors.Is(err, errNoPackage) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	full, err := h.assets.Get(ctx, repo.ID, packumentPath(formFull, pkg))
	switch {
	case errors.Is(err, assets.ErrNotFound):
		full = nil
	case err != nil:
		return nil, err
	}
	fullSHA := ""
	if full != nil {
		fullSHA = full.SHA256
	}
	key := format.DocumentKey(pkg, s.remote.SHA256, fullSHA)
	if l := h.lists.get(key); l != nil {
		return l, nil
	}
	body, err := h.assets.ReadAll(ctx, s.remote, h.assets.MaxUploadSize())
	if err != nil {
		return nil, err
	}
	var doc struct {
		Tags     map[string]string          `json:"dist-tags"`
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errors.New("the remote packument is not valid JSON")
	}
	times := map[string]json.RawMessage{}
	if full != nil {
		fb, err := h.assets.ReadAll(ctx, full, h.assets.MaxUploadSize())
		if err != nil {
			return nil, err
		}
		var fd struct {
			Time map[string]json.RawMessage `json:"time"`
		}
		if json.Unmarshal(fb, &fd) == nil && fd.Time != nil {
			times = fd.Time
		}
	}
	l := &remoteList{latest: doc.Tags["latest"]}
	for v, raw := range doc.Versions {
		sv, err := version.ParseSemVer(v)
		if err != nil {
			continue
		}
		var m struct {
			Dist struct {
				Tarball string `json:"tarball"`
			} `json:"dist"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		file, ok := remoteFile(pkg, m.Dist.Tarball)
		if !ok {
			continue
		}
		rv := remoteVersion{version: v, key: sv.SortKey(), file: file}
		var at string
		if json.Unmarshal(times[v], &at) == nil {
			if t, err := time.Parse(time.RFC3339, at); err == nil {
				rv.published = t.UTC()
			}
		}
		l.versions = append(l.versions, rv)
	}
	slices.SortFunc(l.versions, func(a, b remoteVersion) int { return strings.Compare(b.key, a.key) })
	h.lists.put(key, l)
	return l, nil
}

// remoteTarball returns the stored tarball of rv in repo, or nil when the proxy has not fetched
// it yet.
func (h *Handler) remoteTarball(ctx context.Context, repo *repos.Repo, pkg string, rv remoteVersion) (*assets.Asset, error) {
	a, err := h.assets.Get(ctx, repo.ID, pkg+"/-/"+rv.file)
	if errors.Is(err, assets.ErrNotFound) {
		return nil, nil
	}
	return a, err
}

// proxyVersions lists the versions of pkg in proxy repo, newest first, keyset-paged by after as
// the hosted list is.
func (h *Handler) proxyVersions(ctx context.Context, repo *repos.Repo, art format.Artifact, pkg, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	l, err := h.remoteVersions(ctx, repo, pkg)
	if err != nil || l == nil {
		return nil, nil, "", err
	}
	upper := "\x7f" // above every sort key
	if after != "" {
		sv, err := version.ParseSemVer(after)
		if err != nil {
			return &art, nil, "", nil
		}
		upper = sv.SortKey()
	}
	if limit < 1 {
		return &art, nil, "", nil
	}
	i, _ := slices.BinarySearchFunc(l.versions, upper, func(rv remoteVersion, k string) int { return strings.Compare(k, rv.key) })
	var out []format.Version
	for _, rv := range l.versions[i:] {
		if rv.key >= upper {
			continue
		}
		if len(out) == limit {
			return &art, out, out[len(out)-1].Version, nil
		}
		a, err := h.remoteTarball(ctx, repo, pkg, rv)
		if err != nil {
			return nil, nil, "", err
		}
		out = append(out, format.Version{Version: rv.version, Kind: format.VersionRelease, BuildsCount: 1,
			PublishedAt: rv.published, Remote: a == nil})
	}
	return &art, out, "", nil
}

// proxyBuilds lists the one build of version v of pkg in proxy repo: its tarball, the stored
// file once the proxy has fetched it, else a Remote file without size or SHA-256 (the packument
// names only its SHA-1 and SHA-512).
func (h *Handler) proxyBuilds(ctx context.Context, repo *repos.Repo, art format.Artifact, pkg, v, build, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	l, err := h.remoteVersions(ctx, repo, pkg)
	if err != nil || l == nil {
		return nil, nil, nil, "", err
	}
	rv, ok := l.find(v)
	if !ok {
		return nil, nil, nil, "", nil
	}
	a, err := h.remoteTarball(ctx, repo, pkg, rv)
	if err != nil {
		return nil, nil, nil, "", err
	}
	ver := &format.Version{Version: rv.version, Kind: format.VersionRelease, BuildsCount: 1, PublishedAt: rv.published, Remote: a == nil}
	if after != "" || limit < 1 || build != "" && build != rv.version {
		return &art, ver, nil, "", nil
	}
	f := format.BuildFile{Name: rv.file, Path: pkg + "/-/" + rv.file, Extension: "tgz", Remote: a == nil}
	if a != nil {
		f.Size, f.SHA256, f.UpdatedAt = a.Size, a.SHA256, a.UpdatedAt
	}
	return &art, ver, []format.Build{{Build: rv.version, PublishedAt: rv.published, Files: []format.BuildFile{f}}}, "", nil
}
