package pypi

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/pypi/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// Versions and builds (format.Spec Versions and Builds) of a project of a hosted repo, read from
// pypi_files. The artifact folder is packages/<project>/. A version has one build: its files, the
// sdist and the wheels. Versions page newest first by sort_key (keyset).

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

// versionFiles returns the build of version key of project: its files, its time and its CI build
// record (of the newest file).
func (h *Handler) versionFiles(ctx context.Context, repo *repos.Repo, project, key string) ([]format.BuildFile, time.Time, int64, error) {
	rows, err := h.q.ListVersionFiles(ctx, dbq.ListVersionFilesParams{RepoID: repo.ID, Project: project, SortKey: key})
	if err != nil {
		return nil, time.Time{}, 0, err
	}
	var files []format.BuildFile
	var at time.Time
	var build int64
	for _, x := range rows {
		f := format.BuildFile{Name: x.Filename, Path: x.Path, Size: x.Size, SHA256: x.Sha256, Extension: extOf(x.Filename)}
		if d, err := parseFilename(x.Filename); err == nil {
			f.Classifier = d.tags
		}
		files = append(files, f)
		if t := time.Unix(x.UpdatedAt, 0).UTC(); !t.Before(at) {
			at = t
			if x.BuildID.Valid {
				build = x.BuildID.Int64
			}
		}
	}
	slices.SortFunc(files, func(a, b format.BuildFile) int {
		if (a.Extension == "whl") != (b.Extension == "whl") {
			if a.Extension == "whl" {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return files, at, build, nil
}

// Versions lists the versions of a project folder, newest first.
func (h *Handler) Versions(ctx context.Context, repo *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	art, project, err := h.projectOf(ctx, repo, p)
	if err != nil || art == nil {
		return nil, nil, "", err
	}
	type row struct {
		key, version string
	}
	var rows []row
	if after == "" {
		rs, err := h.q.ListVersions(ctx, dbq.ListVersionsParams{RepoID: repo.ID, Project: project, Limit: int64(limit) + 1})
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
		rs, err := h.q.ListVersionsBefore(ctx, dbq.ListVersionsBeforeParams{RepoID: repo.ID, Project: project, SortKey: v.SortKey(), Limit: int64(limit) + 1})
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
	prov := h.host.Builds.Loader()
	out := make([]format.Version, 0, len(rows))
	for _, r := range rows {
		_, at, build, err := h.versionFiles(ctx, repo, project, r.key)
		if err != nil {
			return nil, nil, "", err
		}
		v := format.Version{Version: r.version, Kind: format.VersionRelease, BuildsCount: 1, PublishedAt: at}
		if v.CI, err = prov.Get(ctx, build); err != nil {
			return nil, nil, "", err
		}
		out = append(out, v)
	}
	return art, out, next, nil
}

// Builds lists the one build of version v of a project folder: its files.
func (h *Handler) Builds(ctx context.Context, repo *repos.Repo, p, v, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	art, project, err := h.projectOf(ctx, repo, p)
	if err != nil || art == nil {
		return nil, nil, nil, "", err
	}
	pv, err := version.ParsePEP440(v)
	if err != nil {
		return nil, nil, nil, "", nil
	}
	files, at, build, err := h.versionFiles(ctx, repo, project, pv.SortKey())
	if err != nil || len(files) == 0 {
		return nil, nil, nil, "", err
	}
	ci, err := h.host.Builds.Loader().Get(ctx, build)
	if err != nil {
		return nil, nil, nil, "", err
	}
	ver := &format.Version{Version: v, Kind: format.VersionRelease, BuildsCount: 1, PublishedAt: at, CI: ci}
	if after != "" || limit < 1 {
		return art, ver, []format.Build{}, "", nil
	}
	return art, ver, []format.Build{{Build: v, Files: files, PublishedAt: at, CI: ci}}, "", nil
}
