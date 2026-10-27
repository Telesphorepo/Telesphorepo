package rpm

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/rpm/dbq"
)

// Versions and builds (format.Spec Versions and Builds) of a package, read from rpm_packages
// through rpm_packages_order. An artifact folder is Packages/<name>/. A version is
// [epoch:]version-release with one build, whose files are its packages, one per architecture. A
// page of versions seeks from one version to the next.

// top is above every order key.
var top = []byte{0xff}

// packageOf splits an artifact folder (Packages/<name>/) into the artifact and the package name.
func packageOf(dir string) (format.Artifact, string, bool) {
	segs := strings.Split(strings.Trim(dir, "/"), "/")
	if len(segs) != 2 || segs[0] != "Packages" || !nameRE.MatchString(segs[1]) {
		return format.Artifact{}, "", false
	}
	return format.Artifact{Path: "Packages/" + segs[1] + "/", Title: segs[1], Name: segs[1], Kind: "package"}, segs[1], true
}

// keyOf is the order key of the version string v ([epoch:]version-release); false when v is not
// one.
func keyOf(v string) ([]byte, bool) {
	e, ver, rel, ok := parseEVR(v)
	if !ok {
		return nil, false
	}
	return versionKey(e, ver, rel), true
}

// version is one version of a package with its files.
type version struct {
	name  string
	key   []byte
	files []dbq.VersionFile
	at    time.Time
	build int64
}

// group gathers the files of a page into versions, newest first.
func group(rows []dbq.VersionFile) []*version {
	by := map[string]*version{}
	var out []*version
	for _, r := range rows {
		v := by[string(r.Vkey)]
		if v == nil {
			v = &version{name: r.Evr, key: r.Vkey}
			by[string(r.Vkey)] = v
			out = append(out, v)
		}
		v.files = append(v.files, r)
		if at := time.Unix(r.UpdatedAt, 0).UTC(); at.After(v.at) {
			v.at = at
		}
		if r.BuildID.Valid && v.build == 0 {
			v.build = r.BuildID.Int64
		}
	}
	slices.SortFunc(out, func(x, y *version) int { return bytes.Compare(y.key, x.key) })
	return out
}

func (h *Handler) summary(ctx context.Context, v *version) (*format.Version, error) {
	out := &format.Version{Version: v.name, Kind: format.VersionRelease, BuildsCount: 1, PublishedAt: v.at}
	var err error
	out.CI, err = h.host.Builds.Loader().Get(ctx, v.build)
	return out, err
}

// Versions lists the versions of a package folder, newest first.
func (h *Handler) Versions(ctx context.Context, _, from *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	art, pkg, ok := packageOf(p)
	if !ok {
		return nil, nil, "", nil
	}
	below := top
	if after != "" {
		k, ok := keyOf(after)
		if !ok {
			return nil, nil, "", nil
		}
		below = k
	}
	rows, err := h.q.VersionsPage(ctx, from.ID, pkg, below, int64(max(limit, 0)+1))
	if err != nil {
		return nil, nil, "", err
	}
	versions := group(rows)
	if len(versions) == 0 {
		if after == "" {
			return nil, nil, "", nil
		}
		has, err := h.q.PackageHasFiles(ctx, dbq.PackageHasFilesParams{RepoID: from.ID, Name: pkg})
		if err != nil || !has {
			return nil, nil, "", err
		}
		return &art, nil, "", nil
	}
	var out []format.Version
	for _, v := range versions {
		if len(out) == limit {
			return &art, out, out[len(out)-1].Version, nil
		}
		s, err := h.summary(ctx, v)
		if err != nil {
			return nil, nil, "", err
		}
		out = append(out, *s)
	}
	return &art, out, "", nil
}

// Builds lists the one build of version name of a package folder: its packages, one per
// architecture.
func (h *Handler) Builds(ctx context.Context, _, from *repos.Repo, p, name, build, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	art, pkg, ok := packageOf(p)
	if !ok {
		return nil, nil, nil, "", nil
	}
	k, ok := keyOf(name)
	if !ok {
		return nil, nil, nil, "", nil
	}
	rows, err := h.q.VersionFiles(ctx, from.ID, pkg, k)
	if err != nil || len(rows) == 0 {
		return nil, nil, nil, "", err
	}
	v := group(rows)[0]
	ver, err := h.summary(ctx, v)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if limit < 1 || after != "" || build != "" && build != v.name {
		return &art, ver, nil, "", nil
	}
	b := format.Build{Build: v.name, PublishedAt: v.at, CI: ver.CI}
	for _, f := range v.files {
		b.Files = append(b.Files, format.BuildFile{Name: strings.TrimPrefix(f.Filename, art.Path), Path: f.Filename,
			Size: f.Size, SHA256: f.Sha256, Classifier: f.Arch, Extension: "rpm"})
	}
	slices.SortFunc(b.Files, func(x, y format.BuildFile) int { return strings.Compare(x.Classifier, y.Classifier) })
	return &art, ver, []format.Build{b}, "", nil
}
