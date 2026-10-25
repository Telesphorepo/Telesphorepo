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
// page of either is one seek past its cursor, and each build on it one range read of its files.

const (
	pluginSuffix  = ".gradle.plugin"
	nonUniqueName = "SNAPSHOT" // the build id of a snapshot file without a timestamp
	filesPage     = 500        // files of a build that one read takes
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

// readBuild reads the files of build b of version v (artifactId id) in version folder dir: one
// range read of the names that start like the build's, page by page.
func (h *Handler) readBuild(ctx context.Context, repoID int64, dir, id, v, b string) (*buildFiles, error) {
	out := &buildFiles{id: b}
	prefix := dir + buildStem(id, v, b)
	for after := ""; ; {
		items, next, err := h.assets.List(ctx, repoID, prefix, after, filesPage)
		if err != nil {
			return nil, err
		}
		for _, a := range items {
			f, ok := parseBuildFile(a.Path)
			if !ok || f.dir != dir || f.build != b {
				continue
			}
			out.classes = append(out.classes, format.BuildFile{Name: a.Path[len(dir):], Path: a.Path, Size: a.Size, SHA256: a.SHA256,
				Classifier: f.classifier, Extension: f.ext, UpdatedAt: a.UpdatedAt})
			if a.UpdatedAt.After(out.at) {
				out.at = a.UpdatedAt
			}
			if a.BuildID != 0 && (out.build == 0 || (f.classifier == "" && f.ext == "pom")) {
				out.build = a.BuildID
			}
		}
		if next == "" {
			return out, nil
		}
		after = next
	}
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

// version summarises version v of artifact folder art (artifactId id), which has count builds,
// from its newest build. It returns the newest build too; nil when the version has none.
func (h *Handler) version(ctx context.Context, repoID int64, art, id, v string, count int64, prov *ci.Loader) (*format.Version, *buildFiles, error) {
	dir := art + v + "/"
	ids, err := h.q.ListBuildsBelow(ctx, dbq.ListBuildsBelowParams{RepoID: repoID, Folder: dir, SortKey: keyTop, Limit: 1})
	if err != nil || len(ids) == 0 {
		return nil, nil, err
	}
	newest, err := h.readBuild(ctx, repoID, dir, id, v, ids[0])
	if err != nil || len(newest.classes) == 0 {
		return nil, nil, err
	}
	out := &format.Version{Version: v, Kind: format.VersionRelease, Folder: dir, BuildsCount: int(count), PublishedAt: newest.at}
	if strings.HasSuffix(v, "-SNAPSHOT") {
		out.Kind, out.LatestBuild, out.LatestLabel = format.VersionSnapshot, newest.id, buildLabel(newest.id)
	}
	if out.CI, err = prov.Get(ctx, newest.build); err != nil {
		return nil, nil, err
	}
	return out, newest, nil
}

// Versions lists the versions of an artifact folder: a page of maven_versions below the cursor.
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
	prov := h.host.Builds.Loader()
	var out []format.Version
	for i, r := range rows {
		if i == limit {
			return &art, out, rows[i-1].Version, nil
		}
		ver, _, err := h.version(ctx, from.ID, art.Path, id, r.Version, r.Builds, prov)
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
// cursor, each with the files of the version folder <artifact folder>/<v>/ that it names. A
// non-empty build keeps only that build.
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
	prov := h.host.Builds.Loader()
	ver, newest, err := h.version(ctx, from.ID, art.Path, id, v, row.Builds, prov)
	if err != nil || ver == nil {
		return nil, nil, nil, "", err
	}
	dir := ver.Folder
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
	var out []format.Build
	for i, b := range ids {
		if i == limit {
			return &art, ver, out, ids[i-1], nil
		}
		bf := newest
		if b != newest.id {
			if bf, err = h.readBuild(ctx, from.ID, dir, id, v, b); err != nil {
				return nil, nil, nil, "", err
			}
		}
		if len(bf.classes) == 0 {
			continue
		}
		pv, err := prov.Get(ctx, bf.build)
		if err != nil {
			return nil, nil, nil, "", err
		}
		files := slices.Clone(bf.classes)
		slices.SortStableFunc(files, func(x, y format.BuildFile) int { return fileRank(x) - fileRank(y) })
		out = append(out, format.Build{Build: b, Label: buildLabel(b), Files: files, PublishedAt: bf.at, CI: pv})
	}
	return &art, ver, out, "", nil
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
