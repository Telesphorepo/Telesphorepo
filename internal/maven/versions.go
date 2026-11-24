package maven

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/maven/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Versions and builds (format.Spec Versions and Builds). An artifact folder is <group path>/<artifactId>/ and
// holds one folder per version. A release version folder holds one build: the files named
// <artifactId>-<version>[-<classifier>].<ext>. A -SNAPSHOT version folder holds timestamped
// builds, <artifactId>-<base>-<yyyyMMdd.HHmmss>-<n>[-<classifier>].<ext>, and maybe a
// non-unique build named like a release. The index (index.go) lists the versions and builds; a
// page of either is one seek past its cursor, and one query range-reads the files of every
// build on it.

const (
	pluginSuffix  = ".gradle.plugin"
	nonUniqueName = "SNAPSHOT" // the build id of a snapshot file without a timestamp
)

// buildRE matches the timestamp and build number at the start of a snapshot file's tail.
var buildRE = regexp.MustCompile(`^([0-9]{8}\.[0-9]{6}-[0-9]+)(.*)$`)

// sideFile reports whether name is a checksum or a signature of another file.
func sideFile(name string) bool {
	for _, ext := range []string{".md5", ".sha1", ".sha256", ".sha512", ".asc", ".sig"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// artifactOf splits an artifact folder ("com/acme/lib/") into its coordinates.
func artifactOf(dir string) (format.Artifact, string, string, bool) {
	segs := strings.Split(strings.Trim(dir, "/"), "/")
	if len(segs) < 2 {
		return format.Artifact{}, "", "", false
	}
	id := segs[len(segs)-1]
	group := strings.Join(segs[:len(segs)-1], ".")
	a := format.Artifact{Path: strings.Join(segs, "/") + "/", Title: group + ":" + id, Name: id, Kind: "library"}
	if plugin, ok := strings.CutSuffix(id, pluginSuffix); ok && plugin == group {
		a.Title, a.Name, a.Kind = plugin, plugin, "gradle-plugin"
	}
	return a, group, id, true
}

// tail splits the rest of a file name after "<artifactId>-<version>" into a classifier and an
// extension: "-sources.jar" -> ("sources", "jar"), ".pom" -> ("", "pom").
func tail(rest string) (classifier, ext string, ok bool) {
	switch {
	case strings.HasPrefix(rest, "."):
		return "", rest[1:], len(rest) > 1
	case strings.HasPrefix(rest, "-"):
		c, e, found := strings.Cut(rest[1:], ".")
		return c, e, found && c != "" && e != ""
	}
	return "", "", false
}

type buildFiles struct {
	id      string
	classes []format.BuildFile
	at      time.Time
	build   int64 // the CI build record of the build's main file, else of any file
}

// buildRef names build b of version v (artifactId id) in version folder dir.
type buildRef struct{ dir, id, v, b string }

func (r buildRef) key() string { return r.dir + "\x00" + r.b }

// readBuilds reads the files of the builds refs, by buildRef.key: one query that range-reads the
// names that start like each build's, for every build of a page at once. Every ref has an entry,
// without files when its build has none.
func (h *Handler) readBuilds(ctx context.Context, repoID int64, refs []buildRef) (map[string]*buildFiles, error) {
	out := make(map[string]*buildFiles, len(refs))
	prefixes := make([]string, 0, len(refs))
	for _, r := range refs {
		if out[r.key()] != nil {
			continue
		}
		out[r.key()] = &buildFiles{id: r.b}
		prefixes = append(prefixes, r.dir+buildStem(r.id, r.v, r.b))
	}
	return out, h.readFiles(ctx, repoID, prefixes, out)
}

// readFiles reads the files under prefixes (one query) into the builds of out (by buildRef.key)
// that they belong to; a file of no build of out is left out.
func (h *Handler) readFiles(ctx context.Context, repoID int64, prefixes []string, out map[string]*buildFiles) error {
	rows, err := h.q.ListFilesUnder(ctx, repoID, prefixes)
	if err != nil {
		return err
	}
	// Path order, as a build lists its files; a path that two prefixes hold comes once.
	slices.SortStableFunc(rows, func(x, y dbq.RangeFile) int { return strings.Compare(x.Path, y.Path) })
	for i, a := range rows {
		if i > 0 && rows[i-1].Path == a.Path {
			continue
		}
		f, ok := parseBuildFile(a.Path)
		if !ok {
			continue
		}
		bf := out[f.dir+"\x00"+f.build]
		if bf == nil {
			continue
		}
		at := time.Unix(a.UpdatedAt, 0).UTC()
		bf.classes = append(bf.classes, format.BuildFile{Name: a.Path[len(f.dir):], Path: a.Path, Size: a.Size, SHA256: a.Sha256,
			Classifier: f.classifier, Extension: f.ext, UpdatedAt: at})
		if at.After(bf.at) {
			bf.at = at
		}
		if a.BuildID != 0 && (bf.build == 0 || (f.classifier == "" && f.ext == "pom")) {
			bf.build = a.BuildID
		}
	}
	return nil
}

// buildLabel is the name of snapshot build id for people: "Build 3" for "20261003.101010-3",
// "Build without a timestamp" for the non-unique build, and "" for a release build.
func buildLabel(id string) string {
	if id == nonUniqueName {
		return "Build without a timestamp"
	}
	if m := buildRE.FindStringSubmatch(id); m != nil && m[2] == "" {
		_, n, _ := strings.Cut(id, "-")
		return "Build " + n
	}
	return ""
}

// version summarises version v of artifact folder art, which has count builds, from its newest
// build; nil when that build has no file. Its CI build record is the Loader's (loaded first).
func version(ctx context.Context, art, v string, count int64, newest *buildFiles, prov *ci.Loader) (*format.Version, error) {
	if newest == nil || len(newest.classes) == 0 {
		return nil, nil
	}
	out := &format.Version{Version: v, Kind: format.VersionRelease, Folder: art + v + "/", BuildsCount: int(count), PublishedAt: newest.at}
	if strings.HasSuffix(v, "-SNAPSHOT") {
		out.Kind, out.LatestBuild, out.LatestLabel = format.VersionSnapshot, newest.id, buildLabel(newest.id)
	}
	var err error
	out.CI, err = prov.Get(ctx, newest.build)
	return out, err
}

// loadCI reads the CI build records of the builds of files into prov, in one query.
func loadCI(ctx context.Context, prov *ci.Loader, files map[string]*buildFiles) error {
	ids := make([]int64, 0, len(files))
	for _, bf := range files {
		ids = append(ids, bf.build)
	}
	return prov.Load(ctx, ids)
}

// Versions lists the versions of an artifact folder: a page of maven_versions below the cursor,
// the newest build of each (one query) and the files of those builds (one query).
func (h *Handler) Versions(ctx context.Context, _, from *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	art, _, id, ok := artifactOf(p)
	if !ok {
		return nil, nil, "", nil
	}
	below := keyTop
	if after != "" {
		below = versionKey(after)
	}
	rows, err := h.q.ListVersionsBelow(ctx, dbq.ListVersionsBelowParams{RepoID: from.ID, Folder: art.Path, SortKey: below, Limit: int64(limit) + 1})
	if err != nil {
		return nil, nil, "", err
	}
	if len(rows) == 0 && after == "" {
		return nil, nil, "", nil // no version: not an artifact folder
	}
	page := rows[:min(len(rows), max(limit, 0))]
	dirs := make([]string, len(page))
	for i, r := range page {
		dirs[i] = art.Path + r.Version + "/"
	}
	newest, err := h.q.NewestBuilds(ctx, from.ID, dirs)
	if err != nil {
		return nil, nil, "", err
	}
	var refs []buildRef
	for i, r := range page {
		if b, ok := newest[dirs[i]]; ok {
			refs = append(refs, buildRef{dirs[i], id, r.Version, b})
		}
	}
	files, err := h.readBuilds(ctx, from.ID, refs)
	if err != nil {
		return nil, nil, "", err
	}
	prov := h.host.Builds.Loader()
	if err := loadCI(ctx, prov, files); err != nil {
		return nil, nil, "", err
	}
	var out []format.Version
	for i, r := range rows {
		if i == limit {
			return &art, out, rows[i-1].Version, nil
		}
		var bf *buildFiles
		if b, ok := newest[dirs[i]]; ok {
			bf = files[buildRef{dirs[i], id, r.Version, b}.key()]
		}
		ver, err := version(ctx, art.Path, r.Version, r.Builds, bf, prov)
		if err != nil {
			return nil, nil, "", err
		}
		if ver != nil {
			out = append(out, *ver)
		}
	}
	return &art, out, "", nil
}

// Builds lists the builds of version v of an artifact folder: a page of maven_builds below the
// cursor, each with the files of the version folder <artifact folder>/<v>/ that it names (one
// query for the builds of the page and the newest build). A non-empty build keeps only that
// build.
func (h *Handler) Builds(ctx context.Context, _, from *repos.Repo, p, v, build, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	art, _, id, ok := artifactOf(p)
	if !ok || v == "" || v == "." || v == ".." || strings.Contains(v, "/") {
		return nil, nil, nil, "", nil
	}
	row, err := h.q.GetVersion(ctx, dbq.GetVersionParams{RepoID: from.ID, Folder: art.Path, SortKey: versionKey(v)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, "", nil
	}
	if err != nil {
		return nil, nil, nil, "", err
	}
	dir := art.Path + v + "/"
	top, err := h.q.ListBuildsBelow(ctx, dbq.ListBuildsBelowParams{RepoID: from.ID, Folder: dir, SortKey: keyTop, Limit: 1})
	if err != nil || len(top) == 0 {
		return nil, nil, nil, "", err
	}
	var ids []string
	switch {
	case build != "":
		_, err := h.q.GetBuild(ctx, dbq.GetBuildParams{RepoID: from.ID, Folder: dir, SortKey: buildKey(build)})
		if err == nil {
			ids = []string{build}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil, "", err
		}
	default:
		below := keyTop
		if after != "" {
			below = buildKey(after)
		}
		if ids, err = h.q.ListBuildsBelow(ctx, dbq.ListBuildsBelowParams{RepoID: from.ID, Folder: dir, SortKey: below, Limit: int64(limit) + 1}); err != nil {
			return nil, nil, nil, "", err
		}
	}
	refs := []buildRef{{dir, id, v, top[0]}}
	for i, b := range ids {
		if i < limit {
			refs = append(refs, buildRef{dir, id, v, b})
		}
	}
	files, err := h.readBuilds(ctx, from.ID, refs)
	if err != nil {
		return nil, nil, nil, "", err
	}
	prov := h.host.Builds.Loader()
	if err := loadCI(ctx, prov, files); err != nil {
		return nil, nil, nil, "", err
	}
	ver, err := version(ctx, art.Path, v, row.Builds, files[refs[0].key()], prov)
	if err != nil || ver == nil {
		return nil, nil, nil, "", err
	}
	next := ""
	if len(ids) > limit {
		ids, next = ids[:limit], ids[limit-1]
	}
	out, err := buildsOf(ctx, dir, id, v, ids, files, prov)
	if err != nil {
		return nil, nil, nil, "", err
	}
	return &art, ver, out, next, nil
}

// buildsOf is the format.Build of each build of ids (of version v of artifactId id in version
// folder dir) that has files in files (by buildRef.key), in the order of ids, with its CI build
// record from prov (loaded first).
func buildsOf(ctx context.Context, dir, id, v string, ids []string, files map[string]*buildFiles, prov *ci.Loader) ([]format.Build, error) {
	var out []format.Build
	for _, b := range ids {
		bf := files[buildRef{dir, id, v, b}.key()]
		if bf == nil || len(bf.classes) == 0 {
			continue
		}
		pv, err := prov.Get(ctx, bf.build)
		if err != nil {
			return nil, err
		}
		files := slices.Clone(bf.classes)
		slices.SortStableFunc(files, func(x, y format.BuildFile) int { return fileRank(x) - fileRank(y) })
		out = append(out, format.Build{Build: b, Label: buildLabel(b), Files: files, PublishedAt: bf.at, CI: pv})
	}
	return out, nil
}

// VersionsBuilds returns versions of an artifact folder, each with all its builds, as Builds
// answers them on one page (format.Spec VersionsBuilds): the version rows, the builds of their
// folders and the files of those folders, one query each, and their CI build records.
func (h *Handler) VersionsBuilds(ctx context.Context, _, from *repos.Repo, p string, versions []string) (map[string]format.VersionBuilds, error) {
	out := map[string]format.VersionBuilds{}
	art, _, id, ok := artifactOf(p)
	if !ok {
		return out, nil
	}
	keys := make([][]byte, 0, len(versions))
	for _, v := range versions {
		if v != "" && v != "." && v != ".." && !strings.Contains(v, "/") {
			keys = append(keys, versionKey(v))
		}
	}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := h.q.ListVersionsIn(ctx, from.ID, art.Path, keys)
	if err != nil {
		return nil, err
	}
	dirs := make([]string, len(rows))
	for i, r := range rows {
		dirs[i] = art.Path + r.Version + "/"
	}
	builds, err := h.q.ListBuildsOfAll(ctx, from.ID, dirs)
	if err != nil {
		return nil, err
	}
	files := map[string]*buildFiles{}
	for i, r := range rows {
		for _, b := range builds[dirs[i]] {
			files[buildRef{dirs[i], id, r.Version, b}.key()] = &buildFiles{id: b}
		}
	}
	if err := h.readFiles(ctx, from.ID, dirs, files); err != nil {
		return nil, err
	}
	prov := h.host.Builds.Loader()
	if err := loadCI(ctx, prov, files); err != nil {
		return nil, err
	}
	for i, r := range rows {
		ids := builds[dirs[i]]
		if len(ids) == 0 {
			continue
		}
		slices.SortFunc(ids, func(a, b string) int { return -compareBuilds(a, b) }) // newest first
		ver, err := version(ctx, art.Path, r.Version, r.Builds, files[buildRef{dirs[i], id, r.Version, ids[0]}.key()], prov)
		if err != nil {
			return nil, err
		}
		if ver == nil {
			continue
		}
		bs, err := buildsOf(ctx, dirs[i], id, r.Version, ids, files, prov)
		if err != nil {
			return nil, err
		}
		a := art
		out[r.Version] = format.VersionBuilds{Artifact: &a, Version: ver, Builds: bs}
	}
	return out, nil
}

// fileRank orders the files of a build: the pom, the main file, the module metadata, then the
// classified files.
func fileRank(f format.BuildFile) int {
	switch {
	case f.Classifier == "" && f.Extension == "pom":
		return 0
	case f.Classifier == "" && f.Extension == "module":
		return 2
	case f.Classifier == "":
		return 1
	}
	return 3
}

// hasVersion reports whether artifact folder art (ending in "/") has version v with a build: one
// lookup of maven_versions.
func (h *Handler) hasVersion(ctx context.Context, repoID int64, art, v string) (bool, error) {
	_, err := h.q.GetVersion(ctx, dbq.GetVersionParams{RepoID: repoID, Folder: art, SortKey: versionKey(v)})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// latestVersion returns the newest release of the artifact folder, else the newest snapshot: one
// seek of maven_versions_latest each.
func (h *Handler) latestVersion(ctx context.Context, repo *repos.Repo, dir string) (string, error) {
	art, _, _, ok := artifactOf(dir)
	if !ok {
		return "", nil
	}
	for _, snapshot := range []int64{0, 1} {
		v, err := h.q.LatestVersion(ctx, dbq.LatestVersionParams{RepoID: repo.ID, Folder: art.Path, Snapshot: snapshot})
		if err == nil {
			return v, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	return "", nil
}
