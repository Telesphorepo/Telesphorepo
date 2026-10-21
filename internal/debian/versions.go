package debian

import (
	"context"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/debian/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"pault.ag/go/debian/version"
)

// Versions and builds (format.Spec Versions and Builds) of a Debian package, read from deb_packages. An
// artifact folder is pool/<component>/<prefix>/<package>/: the versions are those of the
// package of that name in that component whose files lie in the folder. A Debian version
// <base>~SNAPSHOT<sep><stamp> (sep one of . + ~ -, stamp 8 to 14 digits, yyyyMMdd[HHmmss]) is a
// build of the snapshot version <base>~SNAPSHOT; any other version is a release with one build.
// The files of a build are its .deb files, one for each architecture. One indexed read
// (deb_packages_package) loads at most maxPackageRows rows of the package.

const maxPackageRows = 20000

var snapshotRE = regexp.MustCompile(`^(.+~SNAPSHOT)[.+~-]([0-9]{8,14})$`)

// splitSnapshot returns the version that Debian version v is a build of, and the build's stamp
// ("" for a release).
func splitSnapshot(v string) (version, stamp string) {
	if m := snapshotRE.FindStringSubmatch(v); m != nil {
		return m[1], m[2]
	}
	return v, ""
}

// packageOf splits an artifact folder (pool/main/u/urn/) into the artifact, its component and
// its package name.
func packageOf(dir string) (format.Artifact, string, string, bool) {
	segs := strings.Split(strings.Trim(dir, "/"), "/")
	if len(segs) != 4 || segs[0] != "pool" || !packageRE.MatchString(segs[3]) {
		return format.Artifact{}, "", "", false
	}
	return format.Artifact{Path: strings.Join(segs, "/") + "/", Title: segs[3], Name: segs[3], Kind: "package"}, segs[1], segs[3], true
}

// debBuild is one Debian version of a package: its .deb files.
type debBuild struct {
	id    string // the Debian version, e.g. 3.0~SNAPSHOT.20261014211855
	stamp string // "" for a release
	dist  string // a distribution that holds it
	files []format.BuildFile
	at    time.Time
	build int64 // the CI build record; 0 = none
}

// debVersion is the builds of one version, newest first.
type debVersion struct {
	name   string
	builds []*debBuild
}

// readPackage groups the .deb files of the package in artifact folder art into versions, newest
// first.
func (h *Handler) readPackage(ctx context.Context, repoID int64, art format.Artifact, component, pkg string) ([]*debVersion, error) {
	rows, err := h.q.ListPackageFiles(ctx, dbq.ListPackageFilesParams{RepoID: repoID, Component: component, Package: pkg, Limit: maxPackageRows})
	if err != nil {
		return nil, err
	}
	builds := map[string]*debBuild{}
	seen := map[string]bool{} // a file in several distributions counts once
	for _, r := range rows {
		rest, ok := strings.CutPrefix(r.Filename, art.Path)
		if !ok || strings.Contains(rest, "/") || seen[r.Filename] {
			continue
		}
		seen[r.Filename] = true
		b := builds[r.Version]
		if b == nil {
			_, stamp := splitSnapshot(r.Version)
			b = &debBuild{id: r.Version, stamp: stamp, dist: r.Distribution}
			builds[r.Version] = b
		}
		b.files = append(b.files, format.BuildFile{Name: rest, Path: r.Filename, Size: r.Size, SHA256: r.Sha256,
			Classifier: r.Architecture, Extension: strings.TrimPrefix(path.Ext(rest), ".")})
		if at := time.Unix(r.UpdatedAt, 0).UTC(); at.After(b.at) {
			b.at = at
		}
		if r.BuildID.Valid && b.build == 0 {
			b.build = r.BuildID.Int64
		}
	}
	byName := map[string]*debVersion{}
	var out []*debVersion
	for _, b := range builds {
		slices.SortFunc(b.files, func(x, y format.BuildFile) int { return strings.Compare(x.Classifier, y.Classifier) })
		name, _ := splitSnapshot(b.id)
		v := byName[name]
		if v == nil {
			v = &debVersion{name: name}
			byName[name] = v
			out = append(out, v)
		}
		v.builds = append(v.builds, b)
	}
	for _, v := range out {
		slices.SortFunc(v.builds, func(x, y *debBuild) int { return -compareBuilds(x.id, y.id) })
	}
	slices.SortFunc(out, func(x, y *debVersion) int { return -compareVersions(x.name, y.name) })
	return out, nil
}

// summary is the format.Version of v.
func (v *debVersion) summary(ctx context.Context, builds *ci.Loader) (*format.Version, error) {
	newest := v.builds[0]
	out := &format.Version{Version: v.name, Kind: format.VersionRelease, BuildsCount: len(v.builds), PublishedAt: newest.at}
	if newest.stamp != "" {
		out.Kind, out.LatestBuild = format.VersionSnapshot, newest.id
	}
	var err error
	out.CI, err = builds.Get(ctx, newest.build)
	return out, err
}

// Versions lists the versions of a package folder of the pool.
func (h *Handler) Versions(ctx context.Context, repo *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	art, component, pkg, ok := packageOf(p)
	if !ok {
		return nil, nil, "", nil
	}
	versions, err := h.readPackage(ctx, repo.ID, art, component, pkg)
	if err != nil || len(versions) == 0 {
		return nil, nil, "", err
	}
	prov := h.host.Builds.Loader()
	var out []format.Version
	for _, v := range versions {
		if after != "" && compareVersions(v.name, after) >= 0 {
			continue // on an earlier page
		}
		if len(out) == limit {
			return &art, out, out[len(out)-1].Version, nil
		}
		s, err := v.summary(ctx, prov)
		if err != nil {
			return nil, nil, "", err
		}
		out = append(out, *s)
	}
	return &art, out, "", nil
}

// Builds lists the builds of version name of a package folder of the pool. A non-empty build
// keeps only that build.
func (h *Handler) Builds(ctx context.Context, repo *repos.Repo, p, name, build, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	art, component, pkg, ok := packageOf(p)
	if !ok {
		return nil, nil, nil, "", nil
	}
	versions, err := h.readPackage(ctx, repo.ID, art, component, pkg)
	if err != nil {
		return nil, nil, nil, "", err
	}
	i := slices.IndexFunc(versions, func(v *debVersion) bool { return v.name == name })
	if i < 0 {
		return nil, nil, nil, "", nil
	}
	prov := h.host.Builds.Loader()
	ver, err := versions[i].summary(ctx, prov)
	if err != nil {
		return nil, nil, nil, "", err
	}
	var out []format.Build
	for _, b := range versions[i].builds {
		if build != "" && b.id != build || after != "" && compareBuilds(b.id, after) >= 0 {
			continue
		}
		if len(out) == limit {
			return &art, ver, out, out[len(out)-1].Build, nil
		}
		pv, err := prov.Get(ctx, b.build)
		if err != nil {
			return nil, nil, nil, "", err
		}
		out = append(out, format.Build{Build: b.id, Files: slices.Clone(b.files), PublishedAt: b.at, CI: pv})
	}
	return &art, ver, out, "", nil
}

// compareBuilds orders two builds (Debian versions) of one version: by stamp, a stamp of 8
// digits read as midnight; Debian version order breaks a tie.
func compareBuilds(a, b string) int {
	_, sa := splitSnapshot(a)
	_, sb := splitSnapshot(b)
	pad := func(s string) string { return s + strings.Repeat("0", 14-len(s)) }
	if sa != "" && sb != "" {
		if c := strings.Compare(pad(sa), pad(sb)); c != 0 {
			return c
		}
	}
	return compareVersions(a, b)
}

// compareVersions orders two Debian versions as dpkg --compare-versions does. A version that
// does not parse sorts by its bytes.
func compareVersions(a, b string) int {
	va, errA := version.Parse(a)
	vb, errB := version.Parse(b)
	if errA != nil || errB != nil {
		return strings.Compare(a, b)
	}
	return version.Compare(va, vb)
}
