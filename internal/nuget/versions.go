package nuget

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/nuget/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// Versions and builds (format.Spec Versions and Builds) of a package. A hosted repo reads
// nuget_versions; a proxy reads the version list of its remote (the flat container index.json,
// the document the owner check of a group reads) through the read path. An artifact folder is
// v3-flatcontainer/<id>/; its versions are the package versions, newest first by NuGet order,
// keyset-paged on the sort key. A version has one build: its .nupkg and its .nuspec.

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
func (h *Handler) Versions(ctx context.Context, _, from *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	lid, ok := packageOf(p)
	if !ok {
		return nil, nil, "", nil
	}
	switch from.Kind {
	case repos.KindProxy:
		return h.proxyVersions(ctx, from, lid, after, limit)
	case repos.KindHosted:
	default:
		return nil, nil, "", nil
	}
	var rows []versionRow
	if after == "" {
		rs, err := h.q.ListVersionsDesc(ctx, dbq.ListVersionsDescParams{RepoID: from.ID, LowerID: lid, Limit: int64(limit) + 1})
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
		rs, err := h.q.ListVersionsBefore(ctx, dbq.ListVersionsBeforeParams{RepoID: from.ID, LowerID: lid, SortKey: v.SortKey(), Limit: int64(limit) + 1})
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
func (h *Handler) Builds(ctx context.Context, _, from *repos.Repo, p, v, build, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	lid, ok := packageOf(p)
	if !ok {
		return nil, nil, nil, "", nil
	}
	switch from.Kind {
	case repos.KindProxy:
		return h.proxyBuilds(ctx, from, lid, v, build, after, limit)
	case repos.KindHosted:
	default:
		return nil, nil, nil, "", nil
	}
	r, err := h.getVersion(ctx, from.ID, lid, v)
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
	files := []format.BuildFile{{Name: lid + "." + lver + ".nupkg", Path: r.NupkgPath, Size: r.Size, SHA256: r.Sha256, Extension: "nupkg",
		UpdatedAt: time.Unix(r.UpdatedAt, 0).UTC()}}
	if a, err := h.host.Assets.Get(ctx, from.ID, nuspec); err == nil {
		files = append(files, format.BuildFile{Name: lid + ".nuspec", Path: a.Path, Size: a.Size, SHA256: a.SHA256, Extension: "nuspec", UpdatedAt: a.UpdatedAt})
	}
	return &art, &s, []format.Build{{Build: r.Normalized, Files: files, PublishedAt: s.PublishedAt, CI: s.CI}}, "", nil
}

// remoteVersion is one version of a proxy's remote version list: its sort key and its
// normalised form.
type remoteVersion struct {
	Key     string `json:"k"`
	Version string `json:"v"`
}

// proxyList reads the version list of package lid of proxy repo through the read path (fetched
// again after the repo's metadata_ttl), newest first. ok is false when the remote does not have
// the package. The parsed list is kept in the document cache under the SHA-256 of the cached
// document.
func (h *Handler) proxyList(ctx context.Context, repo *repos.Repo, lid string) ([]remoteVersion, bool, error) {
	a, err := h.proxyFlat(ctx, nil, nil, repo, lid)
	if err != nil || a == nil {
		return nil, false, err
	}
	doc, err := h.docs.Render(format.DocumentKey("nuget-flat-versions", a.SHA256), func() ([]byte, error) {
		body, err := h.readDoc(ctx, a)
		if err != nil {
			return nil, err
		}
		var idx struct {
			Versions []string `json:"versions"`
		}
		_ = json.Unmarshal(body, &idx)
		out := []remoteVersion{}
		seen := map[string]bool{}
		for _, s := range idx.Versions {
			v, err := version.ParseNuGet(s)
			if err != nil || seen[v.SortKey()] {
				continue
			}
			seen[v.SortKey()] = true
			out = append(out, remoteVersion{Key: v.SortKey(), Version: v.Normalized()})
		}
		slices.SortFunc(out, func(x, y remoteVersion) int { return strings.Compare(y.Key, x.Key) })
		return json.Marshal(out)
	})
	if err != nil {
		return nil, false, err
	}
	var list []remoteVersion
	if err := json.Unmarshal(doc.Body, &list); err != nil {
		return nil, false, err
	}
	return list, true, nil
}

// proxySummary is the format.Version of version v of package lid of proxy repo: Remote while
// the proxy has not stored its .nupkg.
func (h *Handler) proxySummary(ctx context.Context, repo *repos.Repo, lid string, v remoteVersion) (format.Version, *assets.Asset, error) {
	lver := strings.ToLower(v.Version)
	nupkg, _ := packagePaths(lid, lver)
	s := format.Version{Version: v.Version, Kind: format.VersionRelease, Folder: flatDir + lid + "/" + lver + "/", BuildsCount: 1}
	a, err := h.host.Assets.Get(ctx, repo.ID, nupkg)
	switch {
	case errors.Is(err, assets.ErrNotFound):
		s.Remote = true
		return s, nil, nil
	case err != nil:
		return format.Version{}, nil, err
	}
	return s, a, nil
}

// proxyVersions lists the versions of package lid of proxy repo, newest first, keyset-paged on
// the sort key as the hosted list is.
func (h *Handler) proxyVersions(ctx context.Context, repo *repos.Repo, lid, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	list, ok, err := h.proxyList(ctx, repo, lid)
	if err != nil || !ok {
		return nil, nil, "", err
	}
	start := 0
	if after != "" {
		v, err := version.ParseNuGet(after)
		if err != nil {
			return nil, nil, "", nil
		}
		k := v.SortKey()
		start = sort.Search(len(list), func(i int) bool { return list[i].Key < k })
	}
	art := artifactOf(lid, lid)
	page := list[start:min(start+max(limit, 0), len(list))]
	next := ""
	if len(page) > 0 && start+len(page) < len(list) {
		next = page[len(page)-1].Version
	}
	out := make([]format.Version, 0, len(page))
	for _, v := range page {
		s, _, err := h.proxySummary(ctx, repo, lid, v)
		if err != nil {
			return nil, nil, "", err
		}
		out = append(out, s)
	}
	return &art, out, next, nil
}

// proxyBuilds lists the one build of version v of package lid of proxy repo: its .nupkg and its
// .nuspec, each from the stored file when the proxy has it, else Remote.
func (h *Handler) proxyBuilds(ctx context.Context, repo *repos.Repo, lid, v, build, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	nv, err := version.ParseNuGet(v)
	if err != nil {
		return nil, nil, nil, "", nil
	}
	list, ok, err := h.proxyList(ctx, repo, lid)
	if err != nil || !ok {
		return nil, nil, nil, "", err
	}
	k := nv.SortKey()
	i := sort.Search(len(list), func(i int) bool { return list[i].Key <= k })
	if i == len(list) || list[i].Key != k {
		return nil, nil, nil, "", nil
	}
	art := artifactOf(lid, lid)
	s, nupkgAsset, err := h.proxySummary(ctx, repo, lid, list[i])
	if err != nil {
		return nil, nil, nil, "", err
	}
	if after != "" || limit < 1 || build != "" && build != s.Version {
		return &art, &s, []format.Build{}, "", nil
	}
	lver := strings.ToLower(s.Version)
	nupkg, nuspec := packagePaths(lid, lver)
	files := []format.BuildFile{remoteFile(lid+"."+lver+".nupkg", nupkg, "nupkg", nupkgAsset)}
	a, err := h.host.Assets.Get(ctx, repo.ID, nuspec)
	switch {
	case errors.Is(err, assets.ErrNotFound):
		a = nil
	case err != nil:
		return nil, nil, nil, "", err
	}
	files = append(files, remoteFile(lid+".nuspec", nuspec, "nuspec", a))
	return &art, &s, []format.Build{{Build: s.Version, Files: files, PublishedAt: s.PublishedAt}}, "", nil
}

// remoteFile is a file of a proxy build: from its stored asset a, or Remote when a is nil.
func remoteFile(name, p, ext string, a *assets.Asset) format.BuildFile {
	if a == nil {
		return format.BuildFile{Name: name, Path: p, Extension: ext, Remote: true}
	}
	return format.BuildFile{Name: name, Path: a.Path, Size: a.Size, SHA256: a.SHA256, Extension: ext, UpdatedAt: a.UpdatedAt}
}
