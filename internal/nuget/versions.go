package nuget

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/nuget/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// Versions and builds (format.Spec Versions and Builds) of a package of a hosted repo, read from
// nuget_versions. An artifact folder is v3-flatcontainer/<id>/; its versions are the package
// versions, newest first by NuGet order, keyset-paged on the sort key. A version has one build:
// its .nupkg and its .nuspec.

// packageOf reads the lowered id of an artifact folder v3-flatcontainer/<id>/.
func packageOf(p string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.Trim(p, "/")+"/", flatDir)
	if !ok {
		return "", false
	}
	lid := strings.TrimSuffix(rest, "/")
	if lid == "" || strings.Contains(lid, "/") || !validID(lid) {
		return "", false
	}
	return lid, true
}

// artifactOf is the format.Artifact of package lid with the id as the package writes it.
func artifactOf(lid, id string) format.Artifact {
	return format.Artifact{Path: flatDir + lid + "/", Title: id, Name: id, Kind: "package"}
}

// summary is the format.Version of a version row. Its folder is the one of its .nupkg
// (v3-flatcontainer/<id>/<version>/), which holds the .nuspec too.
func summary(ctx context.Context, builds *ci.Loader, r versionRow) (format.Version, error) {
	b, err := builds.Get(ctx, buildOf(r))
	return format.Version{Version: r.Normalized, Kind: format.VersionRelease, Folder: r.NupkgPath[:strings.LastIndexByte(r.NupkgPath, '/')+1], BuildsCount: 1,
		PublishedAt: time.Unix(r.UpdatedAt, 0).UTC(), CI: b}, err
}

func buildOf(r versionRow) int64 {
	if r.BuildID.Valid {
		return r.BuildID.Int64
	}
	return 0
}

// Versions lists the versions of a package folder, newest first.
func (h *Handler) Versions(ctx context.Context, repo *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	lid, ok := packageOf(p)
	if !ok || repo.Kind != repos.KindHosted {
		return nil, nil, "", nil
	}
	var rows []versionRow
	if after == "" {
		rs, err := h.q.ListVersionsDesc(ctx, dbq.ListVersionsDescParams{RepoID: repo.ID, LowerID: lid, Limit: int64(limit) + 1})
		if err != nil {
			return nil, nil, "", err
		}
		for _, r := range rs {
			rows = append(rows, versionRow(r))
		}
	} else {
		v, err := version.ParseNuGet(after)
		if err != nil {
			return nil, nil, "", nil
		}
		rs, err := h.q.ListVersionsBefore(ctx, dbq.ListVersionsBeforeParams{RepoID: repo.ID, LowerID: lid, SortKey: v.SortKey(), Limit: int64(limit) + 1})
		if err != nil {
			return nil, nil, "", err
		}
		for _, r := range rs {
			rows = append(rows, versionRow(r))
		}
	}
	if len(rows) == 0 && after == "" {
		return nil, nil, "", nil
	}
	id := lid
	if len(rows) > 0 {
		id = rows[0].PackageID
	}
	art := artifactOf(lid, id)
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].Normalized
	}
	loader := h.host.Builds.Loader()
	out := make([]format.Version, 0, len(rows))
	for _, r := range rows {
		s, err := summary(ctx, loader, r)
		if err != nil {
			return nil, nil, "", err
		}
		out = append(out, s)
	}
	return &art, out, next, nil
}

// getVersion reads version v of package lid of a hosted repo: nil when it has none.
func (h *Handler) getVersion(ctx context.Context, repoID int64, lid, v string) (*versionRow, error) {
	nv, err := version.ParseNuGet(v)
	if err != nil {
		return nil, nil
	}
	r, err := h.q.GetVersion(ctx, dbq.GetVersionParams{RepoID: repoID, LowerID: lid, SortKey: nv.SortKey()})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Builds lists the one build of a version: its .nupkg and its .nuspec.
func (h *Handler) Builds(ctx context.Context, repo *repos.Repo, p, v, build, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	lid, ok := packageOf(p)
	if !ok || repo.Kind != repos.KindHosted {
		return nil, nil, nil, "", nil
	}
	r, err := h.getVersion(ctx, repo.ID, lid, v)
	if err != nil || r == nil {
		return nil, nil, nil, "", err
	}
	art := artifactOf(lid, r.PackageID)
	loader := h.host.Builds.Loader()
	s, err := summary(ctx, loader, *r)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if after != "" || limit < 1 || build != "" && build != r.Normalized {
		return &art, &s, []format.Build{}, "", nil
	}
	lver := strings.ToLower(r.Normalized)
	_, nuspec := packagePaths(lid, lver)
	files := []format.BuildFile{{Name: lid + "." + lver + ".nupkg", Path: r.NupkgPath, Size: r.Size, SHA256: r.Sha256, Extension: "nupkg"}}
	if a, err := h.host.Assets.Get(ctx, repo.ID, nuspec); err == nil {
		files = append(files, format.BuildFile{Name: lid + ".nuspec", Path: a.Path, Size: a.Size, SHA256: a.SHA256, Extension: "nuspec"})
	}
	return &art, &s, []format.Build{{Build: r.Normalized, Files: files, PublishedAt: s.PublishedAt, CI: s.CI}}, "", nil
}
