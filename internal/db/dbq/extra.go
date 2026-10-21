package dbq

// Hand-written queries that sqlc cannot express. Each one is a named constant in
// Extra, so the plan guard (db.CheckPlans) checks it like a generated query.

import (
	"context"
	"strings"
)

// Extra maps each hand-written query name to its SQL. A slice parameter is written as
// "/*SLICE:name*/?"; the plan guard checks it with a single placeholder.
var Extra = map[string]string{
	"SearchAssets":         searchAssets,
	"SearchAssetsInRepos":  searchAssetsInRepos,
	"ListSchemaComponents": listSchemaComponents,
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

const searchAssetsInRepos = searchSelect + ` AND a.repo_id IN (/*SLICE:repo_ids*/?)
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

type SearchAssetsInReposParams struct {
	Match   string
	After   int64
	RepoIDs []int64 // must not be empty
	Limit   int64
}

// SearchAssetsInRepos is SearchAssets restricted to RepoIDs.
func (q *Queries) SearchAssetsInRepos(ctx context.Context, arg SearchAssetsInReposParams) ([]SearchAssetsRow, error) {
	if len(arg.RepoIDs) == 0 {
		return nil, nil
	}
	marks := strings.Repeat(",?", len(arg.RepoIDs))[1:]
	query := strings.Replace(searchAssetsInRepos, "/*SLICE:repo_ids*/?", marks, 1)
	args := make([]any, 0, len(arg.RepoIDs)+3)
	args = append(args, arg.Match, arg.After)
	for _, id := range arg.RepoIDs {
		args = append(args, id)
	}
	args = append(args, arg.Limit)
	return q.searchArgs(ctx, query, args...)
}

func (q *Queries) searchArgs(ctx context.Context, query string, args ...any) ([]SearchAssetsRow, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SearchAssetsRow
	for rows.Next() {
		var i SearchAssetsRow
		a := &i.Asset
		if err := rows.Scan(&a.ID, &a.RepoID, &a.Path, &a.BlobID, &a.Size, &a.Sha256, &a.ContentType, &a.Kind,
			&a.UploadedBy, &a.CreatedAt, &a.UpdatedAt, &a.BuildID, &a.Md5, &a.Sha1, &i.RepoName); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// schema_components is created by db.Init, not by schema.sql, so sqlc does not know it.
const listSchemaComponents = `SELECT component, fingerprint FROM schema_components ORDER BY component`

// ListSchemaComponents returns the fingerprint of each schema component of the database: the
// schema state that backup.json records.
func (q *Queries) ListSchemaComponents(ctx context.Context) (map[string]string, error) {
	rows, err := q.db.QueryContext(ctx, listSchemaComponents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, fp string
		if err := rows.Scan(&name, &fp); err != nil {
			return nil, err
		}
		out[name] = fp
	}
	return out, rows.Err()
}
