package debian

import (
	"bytes"
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
)

// Versions and builds (format.Spec Versions and Builds) of a Debian package, read from
// deb_packages through the order keys of deb_package_order (order.go). An artifact folder is
// pool/<component>/<prefix>/<package>/: the versions are those of the package of that name in
// that component whose files lie in the folder. A Debian version <base>~SNAPSHOT<sep><stamp> (sep
// one of . + ~ -, stamp 8 to 14 digits, yyyyMMdd[HHmmss]) is a build of the snapshot version
// <base>~SNAPSHOT; any other version is a release with one build. The files of a build are its
// .deb files, one for each architecture. A page of versions seeks from one version to the next
// and reads the files of each one's newest build; a page of builds reads the files of its builds.

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

// pkgRef is a package folder of a repo: the rows that a page reads.
type pkgRef struct {
	repoID                     int64
	folder, component, pkgName string
}

// top is above every order key.
var top = []byte{0xff}

// debVersion is one version of a package with its newest build.
type debVersion struct {
	name   string
	key    []byte
	builds int
	newest string    // the Debian version of the newest build
	stamp  string    // its stamp; "" for a release
	dist   string    // a distribution that holds it
	at     time.Time // the newest time of its files
	build  int64     // its CI build record; 0 = none
}

// versionsPage returns up to limit versions of package r below the name key below, newest first.
func (h *Handler) versionsPage(ctx context.Context, r pkgRef, below []byte, limit int) ([]*debVersion, error) {
	rows, err := h.q.VersionsPage(ctx, dbq.VersionsPageParams{RepoID: r.repoID, Folder: r.folder, Component: r.component,
		Package: r.pkgName, Below: below, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	byKey := map[string]*debVersion{}
	var out []*debVersion
	for _, row := range rows {
		v := byKey[string(row.NameKey)]
		if v == nil {
			name, stamp := splitSnapshot(row.Version)
			v = &debVersion{name: name, key: row.NameKey, builds: int(row.Builds), newest: row.Version, stamp: stamp, dist: row.Distribution}
			byKey[string(row.NameKey)] = v
			out = append(out, v)
		}
		if at := time.Unix(row.UpdatedAt, 0).UTC(); at.After(v.at) {
			v.at = at
		}
		if row.BuildID.Valid && v.build == 0 {
			v.build = row.BuildID.Int64
		}
	}
	slices.SortFunc(out, func(x, y *debVersion) int { return bytes.Compare(y.key, x.key) })
	return out, nil
}

// versionOf returns version name of package r, or nil.
func (h *Handler) versionOf(ctx context.Context, r pkgRef, name string) (*debVersion, error) {
	k := keyOfName(name)
	vs, err := h.versionsPage(ctx, r, append(slices.Clip(k), 0), 1)
	if err != nil || len(vs) == 0 || !bytes.Equal(vs[0].key, k) {
		return nil, err
	}
	return vs[0], nil
}

// summary is the format.Version of v.
func (v *debVersion) summary(ctx context.Context, builds *ci.Loader) (*format.Version, error) {
	out := &format.Version{Version: v.name, Kind: format.VersionRelease, BuildsCount: v.builds, PublishedAt: v.at}
	if v.stamp != "" {
		out.Kind, out.LatestBuild = format.VersionSnapshot, v.newest
	}
	var err error
	out.CI, err = builds.Get(ctx, v.build)
	return out, err
}

// refOf returns the package folder p of repo.
func refOf(repo *repos.Repo, art format.Artifact, component, pkg string) pkgRef {
	return pkgRef{repoID: repo.ID, folder: art.Path, component: component, pkgName: pkg}
}

// Versions lists the versions of a package folder of the pool.
func (h *Handler) Versions(ctx context.Context, _, from *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	art, component, pkg, ok := packageOf(p)
	if !ok {
		return nil, nil, "", nil
	}
	ref := refOf(from, art, component, pkg)
	below := top
	if after != "" {
		below = keyOfName(after)
	}
	versions, err := h.versionsPage(ctx, ref, below, max(limit, 0)+1)
	if err != nil {
		return nil, nil, "", err
	}
	if len(versions) == 0 {
		if after == "" {
			return nil, nil, "", nil
		}
		has, err := h.q.PackageHasFiles(ctx, dbq.PackageHasFilesParams{RepoID: ref.repoID, Folder: ref.folder, Component: component, Package: pkg})
		if err != nil || !has {
			return nil, nil, "", err
		}
		return &art, nil, "", nil
	}
	prov := h.host.Builds.Loader()
	var out []format.Version
	for _, v := range versions {
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
func (h *Handler) Builds(ctx context.Context, _, from *repos.Repo, p, name, build, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	art, component, pkg, ok := packageOf(p)
	if !ok {
		return nil, nil, nil, "", nil
	}
	ref := refOf(from, art, component, pkg)
	v, err := h.versionOf(ctx, ref, name)
	if err != nil || v == nil {
		return nil, nil, nil, "", err
	}
	prov := h.host.Builds.Loader()
	ver, err := v.summary(ctx, prov)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if limit < 1 {
		return &art, ver, nil, "", nil
	}
	below := top
	if after != "" {
		below = buildKey(after)
	}
	var only []byte
	if build != "" {
		only = buildKey(build)
		if b := append(slices.Clip(only), 0); bytes.Compare(b, below) < 0 {
			below = b
		}
	}
	var out []format.Build
	var key []byte            // the build key of the last build of out
	var buildID int64         // its CI build record
	seen := map[string]bool{} // its files: a file in several distributions counts once
	finish := func() error {
		if len(out) == 0 {
			return nil
		}
		b := &out[len(out)-1]
		slices.SortFunc(b.Files, func(x, y format.BuildFile) int { return strings.Compare(x.Classifier, y.Classifier) })
		var err error
		b.CI, err = prov.Get(ctx, buildID)
		return err
	}
	for r, err := range h.q.StreamBuilds(ctx, dbq.StreamBuildsParams{RepoID: ref.repoID, Folder: ref.folder, Component: component,
		Package: pkg, NameKey: v.key, Below: below}) {
		if err != nil {
			return nil, nil, nil, "", err
		}
		if !bytes.Equal(r.BuildKey, key) {
			if only != nil && !bytes.Equal(r.BuildKey, only) {
				break
			}
			if len(out) == limit {
				if err := finish(); err != nil {
					return nil, nil, nil, "", err
				}
				return &art, ver, out, out[len(out)-1].Build, nil
			}
			if err := finish(); err != nil {
				return nil, nil, nil, "", err
			}
			out = append(out, format.Build{Build: r.Version})
			key, buildID, seen = r.BuildKey, 0, map[string]bool{}
		}
		if seen[r.Filename] {
			continue
		}
		seen[r.Filename] = true
		b := &out[len(out)-1]
		rest := strings.TrimPrefix(r.Filename, art.Path)
		b.Files = append(b.Files, format.BuildFile{Name: rest, Path: r.Filename, Size: r.Size, SHA256: r.Sha256,
			Classifier: r.Architecture, Extension: strings.TrimPrefix(path.Ext(rest), ".")})
		if at := time.Unix(r.UpdatedAt, 0).UTC(); at.After(b.PublishedAt) {
			b.PublishedAt = at
		}
		if r.BuildID.Valid && buildID == 0 {
			buildID = r.BuildID.Int64
		}
	}
	if err := finish(); err != nil {
		return nil, nil, nil, "", err
	}
	return &art, ver, out, "", nil
}
