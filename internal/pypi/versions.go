package pypi

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/pypi/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// Versions and builds (format.Spec Versions and Builds) of a project: of a hosted repo read from
// pypi_files, of a proxy read from the upstream page it caches. The artifact folder is
// packages/<project>/. A version has one build: its files, the sdist and the wheels. Versions page
// newest first by sort key (keyset).

// projectOf splits an artifact folder (packages/<project>/) into its artifact and project, for a
// hosted repo that has the project.
func (h *Handler) projectOf(ctx context.Context, repo *repos.Repo, dir string) (*format.Artifact, string, error) {
	segs := strings.Split(strings.Trim(dir, "/"), "/")
	if repo.Kind != repos.KindHosted || len(segs) != 2 || segs[0] != "packages" {
		return nil, "", nil
	}
	display, err := h.q.GetProject(ctx, dbq.GetProjectParams{RepoID: repo.ID, Project: segs[1]})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return &format.Artifact{Path: "packages/" + segs[1] + "/", Title: display, Name: display, Kind: "project"}, segs[1], nil
}

// sortFiles puts the sdists first, then the wheels, each by name.
func sortFiles(files []format.BuildFile) {
	slices.SortFunc(files, func(a, b format.BuildFile) int {
		if (a.Extension == "whl") != (b.Extension == "whl") {
			if a.Extension == "whl" {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// versionBuild is the one build of a version: its files, its time and its CI build record (of the
// newest file).
type versionBuild struct {
	files []format.BuildFile
	at    time.Time
	build int64
}

// versionFiles returns the builds of the versions of project from sort key lower through upper,
// by sort key: one range read of pypi_files_project, for one version or a page of them.
func (h *Handler) versionFiles(ctx context.Context, repo *repos.Repo, project, lower, upper string) (map[string]*versionBuild, error) {
	rows, err := h.q.ListVersionFiles(ctx, dbq.ListVersionFilesParams{RepoID: repo.ID, Project: project, Lower: lower, Upper: upper})
	if err != nil {
		return nil, err
	}
	out := map[string]*versionBuild{}
	for _, x := range rows {
		vb := out[x.SortKey]
		if vb == nil {
			vb = &versionBuild{}
			out[x.SortKey] = vb
		}
		t := time.Unix(x.UpdatedAt, 0).UTC()
		f := format.BuildFile{Name: x.Filename, Path: x.Path, Size: x.Size, SHA256: x.Sha256, Extension: extOf(x.Filename), UpdatedAt: t}
		if d, err := parseFilename(x.Filename); err == nil {
			f.Classifier = d.tags
		}
		vb.files = append(vb.files, f)
		if !t.Before(vb.at) {
			vb.at = t
			if x.BuildID.Valid {
				vb.build = x.BuildID.Int64
			}
		}
	}
	for _, vb := range out {
		sortFiles(vb.files)
	}
	return out, nil
}

// proxyVersion is a version of a proxy's project page: its sort key, its name (the least of the
// names of its files), its files and the newest upload time the page gives (zero: none).
type proxyVersion struct {
	key, version string
	files        []mfile
	at           time.Time
}

// proxyProject reads the artifact folder (packages/<project>/) of proxy repo from the upstream page
// of the project, through the read path as the owner check of a group read does: the remote's
// page is fetched when the cached one is stale or missing. It returns the artifact and the
// versions the page names, newest first; nil when the remote has no such project.
func (h *Handler) proxyProject(ctx context.Context, repo *repos.Repo, dir string) (*format.Artifact, []*proxyVersion, error) {
	segs := strings.Split(strings.Trim(dir, "/"), "/")
	if repo.Kind != repos.KindProxy || len(segs) != 2 || segs[0] != "packages" || !validName(segs[1]) || normalize(segs[1]) != segs[1] {
		return nil, nil, nil
	}
	n := segs[1]
	a, err := h.upstreamPage(ctx, nil, nil, repo, n)
	if err != nil || a == nil {
		return nil, nil, err
	}
	m, err := h.cachedModel(ctx, repo, n, a)
	if err != nil {
		return nil, nil, err
	}
	by := map[string]*proxyVersion{}
	var out []*proxyVersion
	for _, f := range m.files {
		d, err := parseFilename(f.filename)
		if err != nil {
			continue
		}
		key, v := d.version.SortKey(), d.version.String()
		pv := by[key]
		if pv == nil {
			pv = &proxyVersion{key: key, version: v}
			by[key] = pv
			out = append(out, pv)
		}
		pv.version = min(pv.version, v)
		pv.files = append(pv.files, f)
		if f.uploadTime.After(pv.at) {
			pv.at = f.uploadTime.UTC()
		}
	}
	slices.SortFunc(out, func(a, b *proxyVersion) int { return strings.Compare(b.key, a.key) })
	display := cmp.Or(m.display, n)
	return &format.Artifact{Path: "packages/" + n + "/", Title: display, Name: display, Kind: "project"}, out, nil
}

// stored returns the assets that proxy repo stores of the files of versions, by path: one query.
func (h *Handler) stored(ctx context.Context, repo *repos.Repo, versions []*proxyVersion) (map[string]*assets.Asset, error) {
	var paths []string
	for _, pv := range versions {
		for _, f := range pv.files {
			paths = append(paths, f.local)
		}
	}
	return h.assets.GetMany(ctx, repo.ID, paths)
}

// Versions lists the versions of a project folder, newest first.
func (h *Handler) Versions(ctx context.Context, _, from *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	if from.Kind == repos.KindProxy {
		return h.proxyVersions(ctx, from, p, after, limit)
	}
	art, project, err := h.projectOf(ctx, from, p)
	if err != nil || art == nil {
		return nil, nil, "", err
	}
	type row struct {
		key, version string
	}
	var rows []row
	if after == "" {
		rs, err := h.q.ListVersions(ctx, dbq.ListVersionsParams{RepoID: from.ID, Project: project, Limit: int64(limit) + 1})
		if err != nil {
			return nil, nil, "", err
		}
		for _, r := range rs {
			rows = append(rows, row{r.SortKey, r.Version})
		}
	} else {
		v, err := version.ParsePEP440(after)
		if err != nil {
			return art, []format.Version{}, "", nil
		}
		rs, err := h.q.ListVersionsBefore(ctx, dbq.ListVersionsBeforeParams{RepoID: from.ID, Project: project, SortKey: v.SortKey(), Limit: int64(limit) + 1})
		if err != nil {
			return nil, nil, "", err
		}
		for _, r := range rs {
			rows = append(rows, row{r.SortKey, r.Version})
		}
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].version
	}
	out := make([]format.Version, 0, len(rows))
	if len(rows) == 0 {
		return art, out, next, nil
	}
	// The files of the page (newest first: its keys run from the last row's through the first's)
	// and their CI build records: two queries.
	builds, err := h.versionFiles(ctx, from, project, rows[len(rows)-1].key, rows[0].key)
	if err != nil {
		return nil, nil, "", err
	}
	prov := h.host.Builds.Loader()
	ids := make([]int64, 0, len(builds))
	for _, vb := range builds {
		ids = append(ids, vb.build)
	}
	if err := prov.Load(ctx, ids); err != nil {
		return nil, nil, "", err
	}
	for _, r := range rows {
		vb := builds[r.key]
		if vb == nil {
			vb = &versionBuild{}
		}
		v := format.Version{Version: r.version, Kind: format.VersionRelease, BuildsCount: 1, PublishedAt: vb.at}
		if v.CI, err = prov.Get(ctx, vb.build); err != nil {
			return nil, nil, "", err
		}
		out = append(out, v)
	}
	return art, out, next, nil
}

// proxyVersions lists the versions that the upstream page of a project folder of proxy repo
// names, newest first. A version none of whose files the proxy stores is Remote.
func (h *Handler) proxyVersions(ctx context.Context, repo *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	art, vs, err := h.proxyProject(ctx, repo, p)
	if err != nil || art == nil {
		return nil, nil, "", err
	}
	if after != "" {
		v, err := version.ParsePEP440(after)
		if err != nil {
			return art, []format.Version{}, "", nil
		}
		key := v.SortKey()
		if i := slices.IndexFunc(vs, func(x *proxyVersion) bool { return x.key < key }); i >= 0 {
			vs = vs[i:]
		} else {
			vs = nil
		}
	}
	next := ""
	if len(vs) > limit {
		vs = vs[:max(limit, 0)]
		if limit > 0 {
			next = vs[limit-1].version
		}
	}
	stored, err := h.stored(ctx, repo, vs)
	if err != nil {
		return nil, nil, "", err
	}
	out := make([]format.Version, 0, len(vs))
	for _, pv := range vs {
		remote := !slices.ContainsFunc(pv.files, func(f mfile) bool { return stored[f.local] != nil })
		out = append(out, format.Version{Version: pv.version, Kind: format.VersionRelease, BuildsCount: 1, PublishedAt: pv.at, Remote: remote})
	}
	return art, out, next, nil
}

// Builds lists the one build of version v of a project folder: its files.
func (h *Handler) Builds(ctx context.Context, _, from *repos.Repo, p, v, id, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	if from.Kind == repos.KindProxy {
		return h.proxyBuilds(ctx, from, p, v, id, after, limit)
	}
	art, project, err := h.projectOf(ctx, from, p)
	if err != nil || art == nil {
		return nil, nil, nil, "", err
	}
	pv, err := version.ParsePEP440(v)
	if err != nil {
		return nil, nil, nil, "", nil
	}
	builds, err := h.versionFiles(ctx, from, project, pv.SortKey(), pv.SortKey())
	if err != nil || builds[pv.SortKey()] == nil {
		return nil, nil, nil, "", err
	}
	vb := builds[pv.SortKey()]
	files, at, build := vb.files, vb.at, vb.build
	ci, err := h.host.Builds.Loader().Get(ctx, build)
	if err != nil {
		return nil, nil, nil, "", err
	}
	ver := &format.Version{Version: v, Kind: format.VersionRelease, BuildsCount: 1, PublishedAt: at, CI: ci}
	if after != "" || limit < 1 || id != "" && id != v {
		return art, ver, []format.Build{}, "", nil
	}
	return art, ver, []format.Build{{Build: v, Files: files, PublishedAt: at, CI: ci}}, "", nil
}

// proxyBuilds lists the one build of version v of a project folder of proxy repo: the files that
// its upstream page names, at the paths the proxy serves them from. A file that the proxy stores
// has the stored size, hash and time; another is Remote, with the size and the SHA-256 that the
// page gives (0 and "" when it gives none).
func (h *Handler) proxyBuilds(ctx context.Context, repo *repos.Repo, p, v, id, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	art, vs, err := h.proxyProject(ctx, repo, p)
	if err != nil || art == nil {
		return nil, nil, nil, "", err
	}
	want, err := version.ParsePEP440(v)
	if err != nil {
		return nil, nil, nil, "", nil
	}
	i := slices.IndexFunc(vs, func(x *proxyVersion) bool { return x.key == want.SortKey() })
	if i < 0 {
		return nil, nil, nil, "", nil
	}
	pv := vs[i]
	stored, err := h.stored(ctx, repo, []*proxyVersion{pv})
	if err != nil {
		return nil, nil, nil, "", err
	}
	files := make([]format.BuildFile, 0, len(pv.files))
	remote := true
	for _, x := range pv.files {
		f := format.BuildFile{Name: x.filename, Path: x.local, Extension: extOf(x.filename)}
		if d, err := parseFilename(x.filename); err == nil {
			f.Classifier = d.tags
		}
		if a := stored[x.local]; a != nil {
			f.Size, f.SHA256, f.UpdatedAt = a.Size, a.SHA256, a.UpdatedAt.UTC()
			remote = false
		} else {
			f.Remote, f.Size, f.SHA256 = true, max(x.size, 0), x.hashes["sha256"]
		}
		files = append(files, f)
	}
	sortFiles(files)
	ver := &format.Version{Version: v, Kind: format.VersionRelease, BuildsCount: 1, PublishedAt: pv.at, Remote: remote}
	if after != "" || limit < 1 || id != "" && id != v {
		return art, ver, []format.Build{}, "", nil
	}
	return art, ver, []format.Build{{Build: v, Files: files, PublishedAt: pv.at}}, "", nil
}
