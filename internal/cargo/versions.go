package cargo

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/cargo/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// Versions and builds (format.Spec Versions and Builds) of a crate of a hosted repo, read from
// cargo_versions. The artifact folder of a crate is crates/<name>/; each version is a release
// with one build, its .crate. Proxies and groups keep no rows, so they list none.

// cursorTop is a sort key above every version: the first page.
const cursorTop = "~"

// artifactOf returns the artifact of a crate folder and the crate's lowercased name.
func artifactOf(p string) (format.Artifact, string, bool) {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if len(segs) != 2 || segs[0] != "crates" || !crateNameRE.MatchString(segs[1]) {
		return format.Artifact{}, "", false
	}
	return format.Artifact{Path: "crates/" + segs[1] + "/", Title: segs[1], Name: segs[1], Kind: "crate"}, strings.ToLower(segs[1]), true
}

// versionRow is one version of a crate with its file.
type versionRow struct {
	name, version, path string
	size                int64
	sha256              string
	at                  time.Time
	build               int64
}

func (h *Handler) summary(ctx context.Context, v versionRow) (format.Version, error) {
	pv, err := h.host.Builds.Loader().Get(ctx, v.build)
	return format.Version{Version: v.version, Kind: format.VersionRelease, BuildsCount: 1, PublishedAt: v.at, CI: pv}, err
}

func buildID(n sql.NullInt64) int64 {
	if n.Valid {
		return n.Int64
	}
	return 0
}

// Versions lists the versions of a crate, newest first, keyset by the sort key of after.
func (h *Handler) Versions(ctx context.Context, _, from *repos.Repo, p, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	art, lower, ok := artifactOf(p)
	if !ok || from.Kind != repos.KindHosted {
		return nil, nil, "", nil
	}
	cursor := cursorTop
	if after != "" {
		v, err := version.ParseSemVer(after)
		if err != nil {
			return nil, nil, "", nil
		}
		cursor = v.SortKey()
	}
	rows, err := h.q.ListVersionsPage(ctx, dbq.ListVersionsPageParams{RepoID: from.ID, LowerName: lower, SortKey: cursor, Limit: int64(limit) + 1})
	if err != nil {
		return nil, nil, "", err
	}
	if len(rows) == 0 && after == "" {
		return nil, nil, "", nil
	}
	var out []format.Version
	next := ""
	for i, r := range rows {
		if i == limit {
			next = out[len(out)-1].Version
			break
		}
		art.Title, art.Name = r.Name, r.Name
		s, err := h.summary(ctx, versionRow{version: r.Version, at: time.Unix(r.UpdatedAt, 0).UTC(), build: buildID(r.BuildID)})
		if err != nil {
			return nil, nil, "", err
		}
		out = append(out, s)
	}
	return &art, out, next, nil
}

// getVersion returns one version of a crate; false when the repo has none.
func (h *Handler) getVersion(ctx context.Context, repo *repos.Repo, lower, vers string) (versionRow, bool, error) {
	v, err := version.ParseSemVer(vers)
	if err != nil {
		return versionRow{}, false, nil
	}
	r, err := h.q.GetVersion(ctx, dbq.GetVersionParams{RepoID: repo.ID, LowerName: lower, SortKey: v.SortKey()})
	if errors.Is(err, sql.ErrNoRows) {
		return versionRow{}, false, nil
	} else if err != nil {
		return versionRow{}, false, err
	}
	return versionRow{name: r.Name, version: r.Version, path: r.Path, size: r.Size, sha256: r.Sha256,
		at: time.Unix(r.UpdatedAt, 0).UTC(), build: buildID(r.BuildID)}, true, nil
}

// Builds lists the one build of a version: its .crate.
func (h *Handler) Builds(ctx context.Context, _, from *repos.Repo, p, vers, build, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	art, lower, ok := artifactOf(p)
	if !ok || from.Kind != repos.KindHosted {
		return nil, nil, nil, "", nil
	}
	r, found, err := h.getVersion(ctx, from, lower, vers)
	if err != nil || !found {
		return nil, nil, nil, "", err
	}
	art.Title, art.Name = r.name, r.name
	s, err := h.summary(ctx, r)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if after != "" || limit < 1 || build != "" && build != r.version {
		return &art, &s, []format.Build{}, "", nil
	}
	b := format.Build{Build: r.version, PublishedAt: r.at, CI: s.CI, Files: []format.BuildFile{{Name: path.Base(r.path), Path: r.path,
		Size: r.size, SHA256: r.sha256, Extension: "crate"}}}
	return &art, &s, []format.Build{b}, "", nil
}
