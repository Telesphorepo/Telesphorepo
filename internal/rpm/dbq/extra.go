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
	"StreamIndex":  streamIndex,
	"SignPage":     signPage,
	"VersionsPage": versionsPage,
	"VersionFiles": versionFiles,
}

// The packages of a repo with the parts of their index entries, in the order of
// rpm_packages_order (name, then version).
const streamIndex = `SELECT p.name, p.arch, p.epoch, p.version, p.release, p.sha256, p.size, p.created_at, p.build_time,
  p.installed_size, p.archive_size, p.filename, p.header_start, p.header_len,
  x.text, x.format_a, x.format_b, x.files, x.other
FROM rpm_packages p
JOIN rpm_package_parts x ON x.package_id = p.id
WHERE p.repo_id = ?
ORDER BY p.name, p.vkey`

// IndexRow is one package of the repodata.
type IndexRow struct {
	Name, Arch                           string
	Epoch                                int64
	Version, Release, Sha256             string
	Size, CreatedAt, BuildTime           int64
	InstalledSize, ArchiveSize           int64
	Filename                             string
	HeaderStart, HeaderLen               int64
	Text, FormatA, FormatB, Files, Other string
}

// StreamIndex yields the packages of a repo in order without loading them all. A failed query
// or scan yields one error and stops.
func (q *Queries) StreamIndex(ctx context.Context, repoID int64) iter.Seq2[*IndexRow, error] {
	return func(yield func(*IndexRow, error) bool) {
		rows, err := q.db.QueryContext(ctx, streamIndex, repoID)
		if err != nil {
			yield(nil, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			r := &IndexRow{}
			if err := rows.Scan(&r.Name, &r.Arch, &r.Epoch, &r.Version, &r.Release, &r.Sha256, &r.Size, &r.CreatedAt, &r.BuildTime,
				&r.InstalledSize, &r.ArchiveSize, &r.Filename, &r.HeaderStart, &r.HeaderLen,
				&r.Text, &r.FormatA, &r.FormatB, &r.Files, &r.Other); err != nil {
				yield(nil, err)
				return
			}
			if !yield(r, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(nil, err)
		}
	}
}

// A page of the packages of a repo after (name, vkey, id), through rpm_packages_order (whose
// entries end with the rowid): the signatures of a key change.
const signPage = `SELECT id, name, vkey, filename, format, hdr_state, header_start, size FROM rpm_packages
WHERE repo_id = ?1 AND (name, vkey, id) > (?2, ?3, ?4)
ORDER BY name, vkey, id LIMIT ?5`

// SignRow is one package of SignPage.
type SignRow struct {
	ID          int64
	Name        string
	Vkey        []byte
	Filename    string
	Format      int64
	HdrState    []byte
	HeaderStart int64
	Size        int64
}

// SignPage returns up to limit packages of repoID after the key (name, vkey, id).
func (q *Queries) SignPage(ctx context.Context, repoID int64, name string, vkey []byte, id, limit int64) ([]SignRow, error) {
	rows, err := q.db.QueryContext(ctx, signPage, repoID, name, vkey, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignRow
	for rows.Next() {
		var r SignRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Vkey, &r.Filename, &r.Format, &r.HdrState, &r.HeaderStart, &r.Size); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// A page of the versions of a package below an order key, newest first: the recursive keys seek
// from one key to the next through rpm_packages_order, so the page reads limit versions; each
// brings its files (one per architecture). The CROSS JOIN keeps the page the outer loop.
const versionsPage = `WITH RECURSIVE ks(k) AS (
  SELECT (SELECT max(vkey) FROM rpm_packages WHERE repo_id = ?1 AND name = ?2 AND vkey < ?3)
  UNION ALL
  SELECT (SELECT max(p.vkey) FROM rpm_packages p WHERE p.repo_id = ?1 AND p.name = ?2 AND p.vkey < ks.k)
  FROM ks WHERE ks.k IS NOT NULL
  LIMIT ?4
)
SELECT p.vkey, p.evr, p.arch, p.filename, p.size, p.sha256, a.updated_at, a.build_id
FROM ks
CROSS JOIN rpm_packages p ON p.repo_id = ?1 AND p.name = ?2 AND p.vkey = ks.k
JOIN assets a ON a.id = p.asset_id
WHERE ks.k IS NOT NULL`

// VersionFile is one file of a version of a package.
type VersionFile struct {
	Vkey      []byte
	Evr       string
	Arch      string
	Filename  string
	Size      int64
	Sha256    string
	UpdatedAt int64
	BuildID   sql.NullInt64
}

func scanFiles(rows *sql.Rows) ([]VersionFile, error) {
	defer rows.Close()
	var out []VersionFile
	for rows.Next() {
		var r VersionFile
		if err := rows.Scan(&r.Vkey, &r.Evr, &r.Arch, &r.Filename, &r.Size, &r.Sha256, &r.UpdatedAt, &r.BuildID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// VersionsPage returns the files of up to limit versions of package name below the order key
// below, in no order.
func (q *Queries) VersionsPage(ctx context.Context, repoID int64, name string, below []byte, limit int64) ([]VersionFile, error) {
	rows, err := q.db.QueryContext(ctx, versionsPage, repoID, name, below, limit)
	if err != nil {
		return nil, err
	}
	return scanFiles(rows)
}

// The files of one version of a package, through rpm_packages_order.
const versionFiles = `SELECT p.vkey, p.evr, p.arch, p.filename, p.size, p.sha256, a.updated_at, a.build_id
FROM rpm_packages p
JOIN assets a ON a.id = p.asset_id
WHERE p.repo_id = ? AND p.name = ? AND p.vkey = ?`

// VersionFiles returns the files of the version of package name whose order key is vkey.
func (q *Queries) VersionFiles(ctx context.Context, repoID int64, name string, vkey []byte) ([]VersionFile, error) {
	rows, err := q.db.QueryContext(ctx, versionFiles, repoID, name, vkey)
	if err != nil {
		return nil, err
	}
	return scanFiles(rows)
}
