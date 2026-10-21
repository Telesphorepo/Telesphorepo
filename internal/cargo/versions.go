package cargo

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/cargo/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// Versions and builds (format.Spec Versions and Builds) of a crate. The artifact folder of a crate
// is crates/<name>/; each version is a release with one build, its .crate. A hosted repo reads
// them from cargo_versions; a proxy from the remote's index file of the crate, read through the
// read path as a group read decides the owner of the crate (indexOf). A group keeps no rows.

// cursorTop is a sort key above every version: the first page.
const cursorTop = "~"

// artifactOf returns the artifact of a crate folder and the crate's lowercased name.
func artifactOf(p string) (format.Artifact, string, bool) {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if len(segs) != 2 || segs[0] != "crates" || !crateNameRE.MatchString(segs[1]) {
		return format.Artifact{}, "", false
	}
	return format.Artifact{Path: "crates/" + segs[1] + "/", Title: segs[1], Name: segs[1], Kind: "crate"}, strings.ToLower(segs[1]), true
}

// versionRow is one version of a crate with its file. remote is a proxy's version whose .crate
// the proxy has not stored: size is 0 and sha256 the index's cksum.
type versionRow struct {
	name, version, path string
	size                int64
	sha256              string
	at                  time.Time // the version's publish time
	updated             time.Time // when the stored .crate was last written
	build               int64
	remote              bool
}

func (h *Handler) summary(ctx context.Context, v versionRow) (format.Version, error) {
	pv, err := h.host.Builds.Loader().Get(ctx, v.build)
	return format.Version{Version: v.version, Kind: format.VersionRelease, BuildsCount: 1, PublishedAt: v.at, CI: pv, Remote: v.remote}, err
}

func buildID(n sql.NullInt64) int64 {
	if n.Valid {
		return n.Int64
	}
	return 0
}

// cursorOf is the sort key that a page after the version after starts below; false when after
// is not a version.
func cursorOf(after string) (string, bool) {
	if after == "" {
		return cursorTop, true
	}
	v, err := version.ParseSemVer(after)
	if err != nil {
		return "", false
	}
	return v.SortKey(), true
}

// Versions lists the versions of a crate, newest first, keyset by the sort key of after.
func (h *Handler) Versions(ctx context.Context, _, from *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	art, lower, ok := artifactOf(p)
	if !ok {
		return nil, nil, "", nil
	}
	cursor, ok := cursorOf(after)
	if !ok {
		return nil, nil, "", nil
	}
	var rows []versionRow
	switch from.Kind {
	case repos.KindHosted:
		page, err := h.q.ListVersionsPage(ctx, dbq.ListVersionsPageParams{RepoID: from.ID, LowerName: lower, SortKey: cursor, Limit: int64(limit) + 1})
		if err != nil {
			return nil, nil, "", err
		}
		for _, r := range page {
			rows = append(rows, versionRow{name: r.Name, version: r.Version, at: time.Unix(r.UpdatedAt, 0).UTC(), build: buildID(r.BuildID)})
		}
	case repos.KindProxy:
		all, err := h.proxyVersions(ctx, from, lower)
		if err != nil || all == nil {
			return nil, nil, "", err
		}
		i, found := slices.BinarySearchFunc(all, cursor, byKeyDesc)
		if found {
			i++
		}
		for _, v := range all[i:min(len(all), i+limit+1)] {
			r, err := h.proxyRow(ctx, from, v)
			if err != nil {
				return nil, nil, "", err
			}
			rows = append(rows, r)
		}
	default:
		return nil, nil, "", nil
	}
	if len(rows) == 0 && after == "" {
		return nil, nil, "", nil
	}
	var out []format.Version
	next := ""
	for i, r := range rows {
		if i == limit {
			next = out[len(out)-1].Version
			break
		}
		art.Title, art.Name = r.name, r.name
		s, err := h.summary(ctx, r)
		if err != nil {
			return nil, nil, "", err
		}
		out = append(out, s)
	}
	return &art, out, next, nil
}

// getVersion returns one version of a crate; false when the repo has none. A proxy reads it from
// the remote's index file.
func (h *Handler) getVersion(ctx context.Context, repo *repos.Repo, lower, vers string) (versionRow, bool, error) {
	v, err := version.ParseSemVer(vers)
	if err != nil {
		return versionRow{}, false, nil
	}
	if repo.Kind == repos.KindProxy {
		all, err := h.proxyVersions(ctx, repo, lower)
		if err != nil || all == nil {
			return versionRow{}, false, err
		}
		i, found := slices.BinarySearchFunc(all, v.SortKey(), byKeyDesc)
		if !found {
			return versionRow{}, false, nil
		}
		r, err := h.proxyRow(ctx, repo, all[i])
		return r, err == nil, err
	}
	if repo.Kind != repos.KindHosted {
		return versionRow{}, false, nil
	}
	r, err := h.q.GetVersion(ctx, dbq.GetVersionParams{RepoID: repo.ID, LowerName: lower, SortKey: v.SortKey()})
	if errors.Is(err, sql.ErrNoRows) {
		return versionRow{}, false, nil
	} else if err != nil {
		return versionRow{}, false, err
	}
	at := time.Unix(r.UpdatedAt, 0).UTC()
	return versionRow{name: r.Name, version: r.Version, path: r.Path, size: r.Size, sha256: r.Sha256,
		at: at, updated: at, build: buildID(r.BuildID)}, true, nil
}

// Builds lists the one build of a version: its .crate.
func (h *Handler) Builds(ctx context.Context, _, from *repos.Repo, p, vers, build, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	art, lower, ok := artifactOf(p)
	if !ok {
		return nil, nil, nil, "", nil
	}
	r, found, err := h.getVersion(ctx, from, lower, vers)
	if err != nil || !found {
		return nil, nil, nil, "", err
	}
	art.Title, art.Name = r.name, r.name
	s, err := h.summary(ctx, r)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if after != "" || limit < 1 || build != "" && build != r.version {
		return &art, &s, []format.Build{}, "", nil
	}
	b := format.Build{Build: r.version, PublishedAt: r.at, CI: s.CI, Files: []format.BuildFile{{Name: path.Base(r.path), Path: r.path,
		Size: r.size, SHA256: r.sha256, Extension: "crate", Remote: r.remote, UpdatedAt: r.updated}}}
	return &art, &s, []format.Build{b}, "", nil
}

// proxyVersion is one version that a proxy's index file names.
type proxyVersion struct {
	name, vers, key, cksum string
	at                     time.Time // pubtime of the entry; zero when it has none
}

// byKeyDesc compares v with a sort key in a list of versions newest first.
func byKeyDesc(v proxyVersion, key string) int { return strings.Compare(key, v.key) }

// proxyRow is the row of v: its stored .crate when the proxy has it (one lookup by path), else a
// remote version with the index's cksum.
func (h *Handler) proxyRow(ctx context.Context, repo *repos.Repo, v proxyVersion) (versionRow, error) {
	p := cratePath(v.name, v.vers)
	r := versionRow{name: v.name, version: v.vers, path: p, sha256: v.cksum, at: v.at}
	a, err := h.host.Assets.Get(ctx, repo.ID, p)
	switch {
	case errors.Is(err, assets.ErrNotFound):
		r.remote = true
		return r, nil
	case err != nil:
		return versionRow{}, err
	}
	r.size, r.sha256, r.updated, r.build = a.Size, a.SHA256, a.UpdatedAt, a.BuildID
	return r, nil
}

// proxyVersions returns the versions of crate (lowercase) that the index file of a proxy names,
// newest first, one per sort key: the file as indexOf reads it for a request, fetched when the
// cached one is stale or missing. nil when the remote has no such crate.
func (h *Handler) proxyVersions(ctx context.Context, repo *repos.Repo, crate string) ([]proxyVersion, error) {
	a, err := h.proxyIndex(ctx, nil, nil, repo, crate)
	if err != nil || a == nil {
		return nil, err
	}
	if vs, ok := h.parsed.get(a.SHA256); ok {
		return vs, nil
	}
	b, err := h.host.Assets.ReadAll(ctx, a, maxIndexBytes)
	if err != nil {
		return nil, err
	}
	vs := parseProxyIndex(b)
	h.parsed.put(a.SHA256, vs)
	return vs, nil
}

// parseProxyIndex returns the versions of an index file, newest first, the first line of each
// sort key; a line that is not an entry with a SemVer version is skipped. Never nil.
func parseProxyIndex(b []byte) []proxyVersion {
	out := []proxyVersion{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, maxIndexBytes)
	for sc.Scan() && len(out) < maxIndexRows {
		var e struct {
			Name    string `json:"name"`
			Vers    string `json:"vers"`
			Cksum   string `json:"cksum"`
			Pubtime string `json:"pubtime"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || !crateNameRE.MatchString(e.Name) {
			continue
		}
		v, err := version.ParseSemVer(e.Vers)
		if err != nil {
			continue
		}
		at, _ := time.Parse(time.RFC3339, e.Pubtime)
		out = append(out, proxyVersion{name: e.Name, vers: e.Vers, key: v.SortKey(), cksum: e.Cksum, at: at.UTC()})
	}
	slices.SortStableFunc(out, func(a, b proxyVersion) int { return cmp.Compare(b.key, a.key) })
	return slices.CompactFunc(out, func(a, b proxyVersion) bool { return a.key == b.key })
}

// maxParsed is the most parsed index files that parsedIndexes keeps.
const maxParsed = 256

// parsedIndexes keeps the versions of proxy index files by the SHA-256 of the cached file. Full,
// it drops every entry.
type parsedIndexes struct {
	mu sync.Mutex
	m  map[string][]proxyVersion
}

func (c *parsedIndexes) get(sha string) ([]proxyVersion, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	vs, ok := c.m[sha]
	return vs, ok
}

func (c *parsedIndexes) put(sha string, vs []proxyVersion) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= maxParsed {
		c.m = map[string][]proxyVersion{}
	}
	c.m[sha] = vs
}
