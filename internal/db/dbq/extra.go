package dbq

// Hand-written queries that sqlc cannot express. Each one is a named constant in
// Extra, so the plan guard (db.CheckPlans) checks it like a generated query.

import (
	"context"
	"database/sql"
	"errors"
)

// Extra maps each hand-written query name to its SQL.
var Extra = map[string]string{
	"SearchAssets":       searchAssets,
	"NextSearchMatch":    nextSearchMatch,
	"CountSearchMatches": countSearchMatches,
	"ListMigrations":     listMigrations,
}

// The search index assets_search keys each path by repo_id << SearchShift | asset id, so the
// matches of one repo sit together in rowid order and every query here is a seek in the trigram
// index (sqlc cannot reference rowid or "assets_search MATCH").
const SearchShift = 40

const searchAssets = `SELECT a.id, a.repo_id, a.path, a.blob_id, a.size, a.sha256, a.content_type, a.kind,
  a.uploaded_by, a.created_at, a.updated_at, a.build_id, a.md5, a.sha1, r.name, f.rowid
FROM assets_search f JOIN assets a ON a.id = (f.rowid & 1099511627775) JOIN repos r ON r.id = a.repo_id
WHERE assets_search MATCH ?1 AND f.rowid > ?2 ORDER BY f.rowid LIMIT ?3`

// nextSearchMatch is the key of the first match after a key; no row at the end.
const nextSearchMatch = `SELECT rowid FROM assets_search WHERE assets_search MATCH ?1 AND rowid > ?2 ORDER BY rowid LIMIT 1`

// countSearchMatches counts the matches with a key in [?2, ?3), up to ?4.
const countSearchMatches = `SELECT count(*) FROM (SELECT 1 FROM assets_search WHERE assets_search MATCH ?1 AND rowid >= ?2 AND rowid < ?3 LIMIT ?4)`

// SearchAssetsRow is one search hit: the asset, its repo's name and its key in the index.
type SearchAssetsRow struct {
	Asset    Asset
	RepoName string
	Key      int64
}

// SearchAssets returns the matches of match after the key after, at most limit, in key order: a
// repo's matches together.
func (q *Queries) SearchAssets(ctx context.Context, match string, after, limit int64) ([]SearchAssetsRow, error) {
	rows, err := q.db.QueryContext(ctx, searchAssets, match, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SearchAssetsRow
	for rows.Next() {
		i, err := scanSearchRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// NextSearchMatch returns the key of the first match of match after the key after; ok is false
// at the end.
func (q *Queries) NextSearchMatch(ctx context.Context, match string, after int64) (key int64, ok bool, err error) {
	err = q.db.QueryRowContext(ctx, nextSearchMatch, match, after).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return key, err == nil, err
}

// CountSearchMatches counts the matches of match with a key in [from, to), up to limit.
func (q *Queries) CountSearchMatches(ctx context.Context, match string, from, to, limit int64) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, countSearchMatches, match, from, to, limit).Scan(&n)
	return n, err
}
func scanSearchRow(rows *sql.Rows) (SearchAssetsRow, error) {
	var i SearchAssetsRow
	a := &i.Asset
	err := rows.Scan(&a.ID, &a.RepoID, &a.Path, &a.BlobID, &a.Size, &a.Sha256, &a.ContentType, &a.Kind,
		&a.UploadedBy, &a.CreatedAt, &a.UpdatedAt, &a.BuildID, &a.Md5, &a.Sha1, &i.RepoName, &i.Key)
	return i, err
}

// schema_migrations is created by db.Init, not by a migration, so sqlc does not know it.
const listMigrations = `SELECT component, version, name, checksum FROM schema_migrations ORDER BY component, version`

// AppliedMigration is a row of schema_migrations.
type AppliedMigration struct {
	Component string
	Version   int64
	Name      string
	Checksum  string
}

// ListMigrations returns the applied migrations of the database, by component and version: the
// schema state that backup.json records.
func (q *Queries) ListMigrations(ctx context.Context) ([]AppliedMigration, error) {
	rows, err := q.db.QueryContext(ctx, listMigrations)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppliedMigration
	for rows.Next() {
		var m AppliedMigration
		if err := rows.Scan(&m.Component, &m.Version, &m.Name, &m.Checksum); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
