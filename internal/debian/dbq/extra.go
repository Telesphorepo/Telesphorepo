package dbq

// Hand-written queries that sqlc cannot express. Each one is a named constant in
// Extra, so the plan guard (db.CheckPlans) checks it like a generated query.

import (
	"context"
	"database/sql"
	"iter"
)

// Extra maps each hand-written query name to its SQL.
var Extra = map[string]string{
	"StreamPackages": streamPackages,
	"VersionsPage":   versionsPage,
	"StreamBuilds":   streamBuilds,
}

// A page of the versions of a package folder below a name key, newest first: the recursive
// names seeks from one name key to the next through deb_package_order_page, so the page reads
// limit names; each name brings the files of its newest build and its count of builds. The CROSS
// JOIN keeps the page the outer loop, so each name is one seek.
const versionsPage = `WITH RECURSIVE names(k) AS (
  SELECT (SELECT max(name_key) FROM deb_package_order
    WHERE repo_id = ?1 AND folder = ?2 AND component = ?3 AND package = ?4 AND name_key < ?5)
  UNION ALL
  SELECT (SELECT max(o.name_key) FROM deb_package_order o
    WHERE o.repo_id = ?1 AND o.folder = ?2 AND o.component = ?3 AND o.package = ?4 AND o.name_key < names.k)
  FROM names WHERE names.k IS NOT NULL
  LIMIT ?6
), page(k, b, n) AS MATERIALIZED (
  SELECT k,
    (SELECT max(o.build_key) FROM deb_package_order o
      WHERE o.repo_id = ?1 AND o.folder = ?2 AND o.component = ?3 AND o.package = ?4 AND o.name_key = names.k),
    (SELECT count(DISTINCT o.build_key) FROM deb_package_order o
      WHERE o.repo_id = ?1 AND o.folder = ?2 AND o.component = ?3 AND o.package = ?4 AND o.name_key = names.k)
  FROM names WHERE names.k IS NOT NULL
)
SELECT page.k, page.n, p.version, p.distribution, a.updated_at, a.build_id
FROM page
CROSS JOIN deb_package_order o ON o.repo_id = ?1 AND o.folder = ?2 AND o.component = ?3 AND o.package = ?4
  AND o.name_key = page.k AND o.build_key = page.b
JOIN deb_packages p ON p.id = o.package_id
JOIN assets a ON a.id = p.asset_id`

type VersionsPageParams struct {
	RepoID    int64
	Folder    string
	Component string
	Package   string
	Below     []byte // the name key the page starts below
	Limit     int64
}

// VersionsPageRow is one file of the newest build of a version.
type VersionsPageRow struct {
	NameKey      []byte
	Builds       int64 // the builds of the version
	Version      string
	Distribution string
	UpdatedAt    int64
	BuildID      sql.NullInt64
}

// VersionsPage returns the files of the newest build of each version of a page (VersionsPage),
// in no order.
func (q *Queries) VersionsPage(ctx context.Context, arg VersionsPageParams) ([]VersionsPageRow, error) {
	rows, err := q.db.QueryContext(ctx, versionsPage, arg.RepoID, arg.Folder, arg.Component, arg.Package, arg.Below, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VersionsPageRow
	for rows.Next() {
		var r VersionsPageRow
		if err := rows.Scan(&r.NameKey, &r.Builds, &r.Version, &r.Distribution, &r.UpdatedAt, &r.BuildID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// The files of the builds of one version of a package folder below a build key, newest build
// first, through deb_package_order_page; a file in several distributions has one row for each.
const streamBuilds = `SELECT o.build_key, p.version, p.distribution, p.architecture, p.filename, p.size, p.sha256,
  a.updated_at, a.build_id
FROM deb_package_order o
JOIN deb_packages p ON p.id = o.package_id
JOIN assets a ON a.id = p.asset_id
WHERE o.repo_id = ? AND o.folder = ? AND o.component = ? AND o.package = ? AND o.name_key = ? AND o.build_key < ?
ORDER BY o.build_key DESC`

type StreamBuildsParams struct {
	RepoID    int64
	Folder    string
	Component string
	Package   string
	NameKey   []byte
	Below     []byte // the build key the rows start below
}

type StreamBuildsRow struct {
	BuildKey     []byte
	Version      string
	Distribution string
	Architecture string
	Filename     string
	Size         int64
	Sha256       string
	UpdatedAt    int64
	BuildID      sql.NullInt64
}

// StreamBuilds yields the files of the builds of one version newest build first, so a caller
// stops after a page. A failed query or scan yields one error and stops.
func (q *Queries) StreamBuilds(ctx context.Context, arg StreamBuildsParams) iter.Seq2[StreamBuildsRow, error] {
	return func(yield func(StreamBuildsRow, error) bool) {
		rows, err := q.db.QueryContext(ctx, streamBuilds, arg.RepoID, arg.Folder, arg.Component, arg.Package, arg.NameKey, arg.Below)
		if err != nil {
			yield(StreamBuildsRow{}, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var r StreamBuildsRow
			if err := rows.Scan(&r.BuildKey, &r.Version, &r.Distribution, &r.Architecture, &r.Filename, &r.Size, &r.Sha256,
				&r.UpdatedAt, &r.BuildID); err != nil {
				yield(StreamBuildsRow{}, err)
				return
			}
			if !yield(r, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(StreamBuildsRow{}, err)
		}
	}
}

// The deb_packages UNIQUE index (repo_id, distribution, component, architecture, package,
// version) delivers the rows in (package, version) order, so no sort is needed.
const streamPackages = `SELECT package, version, control, filename, size, md5, sha1, sha256
FROM deb_packages
WHERE repo_id = ? AND distribution = ? AND component = ? AND architecture = ?
ORDER BY package, version`

type StreamPackagesParams struct {
	RepoID       int64
	Distribution string
	Component    string
	Architecture string
}

type StreamPackagesRow struct {
	Package  string
	Version  string
	Control  string // control stanza without hash, Filename and Size fields
	Filename string
	Size     int64
	Md5      string
	Sha1     string
	Sha256   string
}

// StreamPackages yields the packages of one (dist, component, architecture) in (package,
// version) order without loading them all. A failed query or scan yields one error and stops.
func (q *Queries) StreamPackages(ctx context.Context, arg StreamPackagesParams) iter.Seq2[StreamPackagesRow, error] {
	return func(yield func(StreamPackagesRow, error) bool) {
		rows, err := q.db.QueryContext(ctx, streamPackages, arg.RepoID, arg.Distribution, arg.Component, arg.Architecture)
		if err != nil {
			yield(StreamPackagesRow{}, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var r StreamPackagesRow
			if err := rows.Scan(&r.Package, &r.Version, &r.Control, &r.Filename, &r.Size, &r.Md5, &r.Sha1, &r.Sha256); err != nil {
				yield(StreamPackagesRow{}, err)
				return
			}
			if !yield(r, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(StreamPackagesRow{}, err)
		}
	}
}
