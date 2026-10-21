package dbq

// Hand-written queries that sqlc cannot express. Each one is a named constant in
// Extra, so the plan guard (db.CheckPlans) checks it like a generated query.

import (
	"context"
	"database/sql"
)

// Extra maps each hand-written query name to its SQL.
var Extra = map[string]string{
	"SearchAssets":   searchAssets,
	"ListMigrations": listMigrations,
}

// The FTS5 trigram search walks assets_fts in rowid order, which is the keyset order, so no
// sort is needed: sqlc cannot reference rowid or "assets_fts MATCH", and its fallback sorts
// with a temp B-tree.
const searchSelect = `SELECT a.id, a.repo_id, a.path, a.blob_id, a.size, a.sha256, a.content_type, a.kind,
  a.uploaded_by, a.created_at, a.updated_at, a.build_id, a.md5, a.sha1, r.name
FROM assets_fts f JOIN assets a ON a.id = f.rowid JOIN repos r ON r.id = a.repo_id
WHERE assets_fts MATCH ? AND f.rowid > ?`

const searchAssets = searchSelect + `
ORDER BY f.rowid LIMIT ?`

// SearchAssetsRow is one search hit: the asset and its repo's name.
type SearchAssetsRow struct {
	Asset    Asset
	RepoName string
}

type SearchAssetsParams struct {
	Match string // an FTS5 query, e.g. the search text quoted as a phrase
	After int64  // asset id of the last hit of the previous page; 0 for the first page
	Limit int64
}

// SearchAssets returns assets whose path matches, across every repo, in asset id order.
func (q *Queries) SearchAssets(ctx context.Context, arg SearchAssetsParams) ([]SearchAssetsRow, error) {
	return q.searchArgs(ctx, searchAssets, arg.Match, arg.After, arg.Limit)
}

// WalkSearchAssets reads the matches of SearchAssets after the asset id after, in asset id
// order, at most limit of them, and gives each to fn until fn returns false. It returns how many
// it read and the asset id of the last one (0 for none). A search restricted to some repos
// filters in fn: it reads a bounded window of the index, however few of its matches are in
// those repos.
func (q *Queries) WalkSearchAssets(ctx context.Context, match string, after, limit int64, fn func(SearchAssetsRow) bool) (int64, int64, error) {
	rows, err := q.db.QueryContext(ctx, searchAssets, match, after, limit)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var read, last int64
	for rows.Next() {
		i, err := scanSearchRow(rows)
		if err != nil {
			return read, last, err
		}
		read++
		last = i.Asset.ID
		if !fn(i) {
			break
		}
	}
	return read, last, rows.Err()
}

func (q *Queries) searchArgs(ctx context.Context, query string, args ...any) ([]SearchAssetsRow, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func scanSearchRow(rows *sql.Rows) (SearchAssetsRow, error) {
	var i SearchAssetsRow
	a := &i.Asset
	err := rows.Scan(&a.ID, &a.RepoID, &a.Path, &a.BlobID, &a.Size, &a.Sha256, &a.ContentType, &a.Kind,
		&a.UploadedBy, &a.CreatedAt, &a.UpdatedAt, &a.BuildID, &a.Md5, &a.Sha1, &i.RepoName)
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
