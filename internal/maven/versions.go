package maven

import (
	"context"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/provenance"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Versions and builds (format.Versioned). An artifact folder is <group path>/<artifactId>/ and
// holds one folder per version. A release version folder holds one build: the files named
// <artifactId>-<version>[-<classifier>].<ext>. A -SNAPSHOT version folder holds timestamped
// builds, <artifactId>-<base>-<yyyyMMdd.HHmmss>-<n>[-<classifier>].<ext>, and maybe a
// non-unique build named like a release. Each version and build costs a few indexed seeks; a
// folder with more children than maxChildren lists the first maxChildren.

const (
	maxChildren   = 5000 // version folders of an artifact, or files of a version folder
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
	a := format.Artifact{Path: strings.Join(segs, "/") + "/", Title: group + ":" + id, Kind: "library"}
	if plugin, ok := strings.CutSuffix(id, pluginSuffix); ok && plugin == group {
		a.Title, a.Kind = plugin, "gradle-plugin"
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

// childDirs returns the names of the folders directly under dir, in path order, one seek each.
func (h *Handler) childDirs(ctx context.Context, repoID int64, dir string) ([]string, error) {
	upper, _ := assets.PrefixEnd(dir)
	lower, inclusive := dir, true
	var out []string
	for range maxChildren * 2 {
		a, err := h.assets.Seek(ctx, repoID, lower, inclusive, upper)
		if err != nil || a == nil || len(out) >= maxChildren {
			return out, err
		}
		rest := a.Path[len(dir):]
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			out = append(out, rest[:i])
			lower, _ = assets.PrefixEnd(dir + rest[:i] + "/")
			inclusive = true
			continue
		}
		lower, inclusive = a.Path, false
	}
	return out, nil
}

// folderFiles returns the files directly in dir (one range scan, at most maxChildren).
func (h *Handler) folderFiles(ctx context.Context, repoID int64, dir string) ([]*assets.Asset, error) {
	items, _, err := h.assets.List(ctx, repoID, dir, "", maxChildren)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(items, func(a *assets.Asset) bool { return strings.Contains(a.Path[len(dir):], "/") }), nil
}

// buildSet is the builds of one version folder, newest first.
type buildSet struct {
	builds []*buildFiles
}

type buildFiles struct {
	id      string
	files   []*assets.Asset
	classes []format.BuildFile
	at      time.Time
	session int64 // the session of the build's main file, else of any file
}

// hasFiles reports whether version folder dir holds a file of version v of artifact id: one
// seek, so a folder that is not a version costs no listing.
func (h *Handler) hasFiles(ctx context.Context, repoID int64, dir, id, v string) (bool, error) {
	prefix := dir + id + "-" + strings.TrimSuffix(v, "-SNAPSHOT")
	end, _ := assets.PrefixEnd(prefix)
	a, err := h.assets.Seek(ctx, repoID, prefix, true, end)
	return a != nil, err
}

// readBuilds groups the files of version folder dir (artifactId id, version v) into builds.
func (h *Handler) readBuilds(ctx context.Context, repoID int64, dir, id, v string) (*buildSet, error) {
	if ok, err := h.hasFiles(ctx, repoID, dir, id, v); err != nil || !ok {
		return &buildSet{}, err
	}
	files, err := h.folderFiles(ctx, repoID, dir)
	if err != nil {
		return nil, err
	}
	snapshot := strings.HasSuffix(v, "-SNAPSHOT")
	stamped := id + "-" + strings.TrimSuffix(v, "-SNAPSHOT") + "-"
	plain := id + "-" + v
	byID := map[string]*buildFiles{}
	var order []*buildFiles
	for _, a := range files {
		name := path.Base(a.Path)
		if sideFile(name) {
			continue
		}
		var build, rest string
		switch {
		case strings.HasPrefix(name, plain) && (len(name) == len(plain) || name[len(plain)] == '.' || name[len(plain)] == '-'):
			build, rest = v, name[len(plain):]
			if snapshot {
				build = nonUniqueName
			}
		case snapshot && strings.HasPrefix(name, stamped):
			m := buildRE.FindStringSubmatch(name[len(stamped):])
			if m == nil {
				continue
			}
			build, rest = m[1], m[2]
		default:
			continue
		}
		classifier, ext, ok := tail(rest)
		if !ok {
			continue
		}
		b := byID[build]
		if b == nil {
			b = &buildFiles{id: build}
			byID[build] = b
			order = append(order, b)
		}
		b.files = append(b.files, a)
		b.classes = append(b.classes, format.BuildFile{Name: name, Path: a.Path, Size: a.Size, SHA256: a.SHA256,
			Classifier: classifier, Extension: ext})
		if a.UpdatedAt.After(b.at) {
			b.at = a.UpdatedAt
		}
		if a.PublishSessionID != 0 && (b.session == 0 || (classifier == "" && ext == "pom")) {
			b.session = a.PublishSessionID
		}
	}
	// Newest first: a timestamp sorts by time, and its build number breaks a tie; the non-unique
	// build goes last.
	slices.SortFunc(order, func(x, y *buildFiles) int { return -compareBuilds(x.id, y.id) })
	return &buildSet{builds: order}, nil
}

// compareBuilds orders snapshot build ids: "SNAPSHOT" first, then by timestamp and number.
func compareBuilds(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == nonUniqueName:
		return -1
	case b == nonUniqueName:
		return 1
	}
	ta, na, _ := strings.Cut(a, "-")
	tb, nb, _ := strings.Cut(b, "-")
	if c := strings.Compare(ta, tb); c != 0 {
		return c
	}
	return compareNum(strings.TrimLeft(na, "0"), strings.TrimLeft(nb, "0"))
}

// version summarises the builds of version v in folder dir. ok is false when it has none.
func (h *Handler) version(ctx context.Context, repoID int64, dir, id, v string, prov *provenance.Loader) (*format.Version, *buildSet, error) {
	bs, err := h.readBuilds(ctx, repoID, dir, id, v)
	if err != nil || len(bs.builds) == 0 {
		return nil, nil, err
	}
	newest := bs.builds[0]
	out := &format.Version{Version: v, Path: dir, Kind: format.VersionRelease, BuildsCount: len(bs.builds), PublishedAt: newest.at}
	if strings.HasSuffix(v, "-SNAPSHOT") {
		out.Kind, out.LatestBuild = format.VersionSnapshot, newest.id
	}
	if out.Provenance, err = prov.Get(ctx, newest.session); err != nil {
		return nil, nil, err
	}
	return out, bs, nil
}

// Versions implements format.Versioned for an artifact folder.
func (h *Handler) Versions(ctx context.Context, repo *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	art, _, id, ok := artifactOf(p)
	if !ok {
		return nil, nil, "", nil
	}
	names, err := h.childDirs(ctx, repo.ID, art.Path)
	if err != nil {
		return nil, nil, "", err
	}
	slices.SortFunc(names, func(a, b string) int { return -compareVersions(a, b) })
	prov := provenance.NewLoader(dbq.New(h.host.DB.R))
	var out []format.Version
	for _, v := range names {
		if after != "" && compareVersions(v, after) >= 0 {
			continue // on an earlier page
		}
		if len(out) == limit {
			return &art, out, out[len(out)-1].Version, nil
		}
		ver, _, err := h.version(ctx, repo.ID, art.Path+v+"/", id, v, prov)
		if err != nil {
			return nil, nil, "", err
		}
		if ver != nil {
			out = append(out, *ver)
		}
	}
	if len(out) == 0 && after == "" {
		return nil, nil, "", nil // no version: not an artifact folder
	}
	return &art, out, "", nil
}

// Builds implements format.Versioned for a version folder.
func (h *Handler) Builds(ctx context.Context, repo *repos.Repo, p, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if len(segs) < 3 {
		return nil, nil, nil, "", nil
	}
	v := segs[len(segs)-1]
	art, _, id, ok := artifactOf(strings.Join(segs[:len(segs)-1], "/"))
	if !ok {
		return nil, nil, nil, "", nil
	}
	prov := provenance.NewLoader(dbq.New(h.host.DB.R))
	ver, bs, err := h.version(ctx, repo.ID, art.Path+v+"/", id, v, prov)
	if err != nil || ver == nil {
		return nil, nil, nil, "", err
	}
	var out []format.Build
	for _, b := range bs.builds {
		if after != "" && compareBuilds(b.id, after) >= 0 {
			continue
		}
		if len(out) == limit {
			return &art, ver, out, out[len(out)-1].Build, nil
		}
		pv, err := prov.Get(ctx, b.session)
		if err != nil {
			return nil, nil, nil, "", err
		}
		files := slices.Clone(b.classes)
		slices.SortFunc(files, func(x, y format.BuildFile) int { return fileRank(x) - fileRank(y) })
		out = append(out, format.Build{Build: b.id, Files: files, PublishedAt: b.at, Provenance: pv})
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

// latestVersion returns the newest release of the artifact folder, else the newest snapshot.
func (h *Handler) latestVersion(ctx context.Context, repo *repos.Repo, dir string) (string, error) {
	_, versions, _, err := h.Versions(ctx, repo, dir, "", maxChildren)
	if err != nil {
		return "", err
	}
	for _, v := range versions {
		if v.Kind == format.VersionRelease {
			return v.Version, nil
		}
	}
	if len(versions) > 0 {
		return versions[0].Version, nil
	}
	return "", nil
}
