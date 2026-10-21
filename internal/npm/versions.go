package npm

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/npm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// Versions and builds (format.Spec Versions and Builds) of a package of a hosted repo, read from
// npm_versions. An artifact folder is <name>/ (<scope>/<name>/ for a scoped name). Every version is
// a release with one build: its tarball. Versions page newest first by sort_key.

// artifactOf returns the artifact of folder p (with a trailing slash), or ok=false.
func artifactOf(p string) (format.Artifact, string, bool) {
	pkg := strings.TrimSuffix(p, "/")
	if !validName(pkg) {
		return format.Artifact{}, "", false
	}
	return format.Artifact{Path: pkg + "/", Title: pkg, Name: unscoped(pkg), Kind: "package"}, pkg, true
}

// Versions lists the versions of a package folder, newest first.
func (h *Handler) Versions(ctx context.Context, _, from *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	art, pkg, ok := artifactOf(p)
	if !ok || from.Kind != repos.KindHosted {
		return nil, nil, "", nil
	}
	if _, err := packageOf(ctx, h.q, from.ID, pkg); err != nil {
		if err == errNoPackage {
			err = nil
		}
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
	rows, err := h.q.ListVersionsBefore(ctx, dbq.ListVersionsBeforeParams{RepoID: from.ID, Name: pkg, SortKey: upper, Limit: int64(limit) + 1})
	if err != nil {
		return nil, nil, "", err
	}
	builds := h.host.Builds.Loader()
	var out []format.Version
	for i, r := range rows {
		if i == limit {
			return &art, out, out[len(out)-1].Version, nil
		}
		b, err := builds.Get(ctx, r.BuildID.Int64)
		if err != nil {
			return nil, nil, "", err
		}
		out = append(out, format.Version{Version: r.Version, Kind: format.VersionRelease, BuildsCount: 1,
			PublishedAt: time.Unix(r.PublishedAt, 0).UTC(), CI: b})
	}
	return &art, out, "", nil
}

// Builds lists the one build of version v of a package folder: its tarball.
func (h *Handler) Builds(ctx context.Context, _, from *repos.Repo, p, v, build, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	art, pkg, ok := artifactOf(p)
	if !ok || from.Kind != repos.KindHosted {
		return nil, nil, nil, "", nil
	}
	r, err := h.q.GetVersionFile(ctx, dbq.GetVersionFileParams{RepoID: from.ID, Name: pkg, Version: v})
	if isNoRows(err) {
		return nil, nil, nil, "", nil
	}
	if err != nil {
		return nil, nil, nil, "", err
	}
	b, err := h.host.Builds.Loader().Get(ctx, r.BuildID.Int64)
	if err != nil {
		return nil, nil, nil, "", err
	}
	at := time.Unix(r.PublishedAt, 0).UTC()
	ver := &format.Version{Version: r.Version, Kind: format.VersionRelease, BuildsCount: 1, PublishedAt: at, CI: b}
	if after != "" || limit < 1 || build != "" && build != r.Version {
		return &art, ver, nil, "", nil
	}
	return &art, ver, []format.Build{{Build: r.Version, PublishedAt: at, CI: b, Files: []format.BuildFile{{
		Name: path.Base(r.Tarball), Path: r.Tarball, Size: r.Size, SHA256: r.Sha256, Extension: "tgz"}}}}, "", nil
}
