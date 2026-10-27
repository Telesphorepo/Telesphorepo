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
	"StreamEntries": streamEntries,
	"StreamArch":    streamArch,
	"VersionsPage":  versionsPage,
	"VersionFiles":  versionFiles,
}

// The entries of the index of one architecture in name order (the primary key of
// arch_db_entries), each with its package's desc and file list.
const streamEntries = `SELECT p.name, p.version, p.desc, f.files
FROM arch_db_entries e
JOIN arch_packages p ON p.id = e.package_id
JOIN arch_package_files f ON f.package_id = p.id
WHERE e.repo_id = ? AND e.arch = ?
ORDER BY e.name`

// StreamEntriesRow is one entry of an index.
type StreamEntriesRow struct {
	Name, Version, Desc, Files string
}

// StreamEntries yields the entries of the index of arch in name order without loading them all.
// A failed query or scan yields one error and stops.
func (q *Queries) StreamEntries(ctx context.Context, repoID int64, arch string) iter.Seq2[StreamEntriesRow, error] {
	return func(yield func(StreamEntriesRow, error) bool) {
		rows, err := q.db.QueryContext(ctx, streamEntries, repoID, arch)
		if err != nil {
			yield(StreamEntriesRow{}, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var r StreamEntriesRow
			if err := rows.Scan(&r.Name, &r.Version, &r.Desc, &r.Files); err != nil {
				yield(StreamEntriesRow{}, err)
				return
			}
			if !yield(r, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(StreamEntriesRow{}, err)
		}
	}
}

// The packages of one architecture of a repo by name, oldest version first within a name,
// through arch_packages_arch.
const streamArch = `SELECT id, name, vkey FROM arch_packages
WHERE repo_id = ? AND arch = ?
ORDER BY name, vkey`

// StreamArchRow is one package of StreamArch.
type StreamArchRow struct {
	ID   int64
	Name string
	Vkey []byte
}

// StreamArch yields the packages of arch in (name, version) order. A failed query or scan yields
// one error and stops.
func (q *Queries) StreamArch(ctx context.Context, repoID int64, arch string) iter.Seq2[StreamArchRow, error] {
	return func(yield func(StreamArchRow, error) bool) {
		rows, err := q.db.QueryContext(ctx, streamArch, repoID, arch)
		if err != nil {
			yield(StreamArchRow{}, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var r StreamArchRow
			if err := rows.Scan(&r.ID, &r.Name, &r.Vkey); err != nil {
				yield(StreamArchRow{}, err)
				return
			}
			if !yield(r, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(StreamArchRow{}, err)
		}
	}
}

// A page of the versions of a package below an order key, newest first: the recursive keys seek
// from one key to the next through arch_packages_order, so the page reads limit versions; each
// brings its files (one per architecture). The CROSS JOIN keeps the page the outer loop.
const versionsPage = `WITH RECURSIVE ks(k) AS (
  SELECT (SELECT max(vkey) FROM arch_packages WHERE repo_id = ?1 AND name = ?2 AND vkey < ?3)
  UNION ALL
  SELECT (SELECT max(p.vkey) FROM arch_packages p WHERE p.repo_id = ?1 AND p.name = ?2 AND p.vkey < ks.k)
  FROM ks WHERE ks.k IS NOT NULL
  LIMIT ?4
)
SELECT p.vkey, p.version, p.arch, p.filename, p.size, p.sha256, a.updated_at, a.build_id
FROM ks
CROSS JOIN arch_packages p ON p.repo_id = ?1 AND p.name = ?2 AND p.vkey = ks.k
JOIN assets a ON a.id = p.asset_id
WHERE ks.k IS NOT NULL`

// VersionFile is one file of a version of a package.
type VersionFile struct {
	Vkey      []byte
	Version   string
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
		if err := rows.Scan(&r.Vkey, &r.Version, &r.Arch, &r.Filename, &r.Size, &r.Sha256, &r.UpdatedAt, &r.BuildID); err != nil {
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

// The files of one version of a package, through arch_packages_order.
const versionFiles = `SELECT p.vkey, p.version, p.arch, p.filename, p.size, p.sha256, a.updated_at, a.build_id
FROM arch_packages p
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
