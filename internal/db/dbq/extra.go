package dbq

// Hand-written queries that sqlc cannot express. Each one is a named constant in
// Extra, so the plan guard (db.CheckPlans) checks it like a generated query.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Extra maps each hand-written query name to its SQL.
var Extra = map[string]string{
	"SearchAssets":       searchAssets,
	"NextSearchMatch":    nextSearchMatch,
	"CountSearchMatches": countSearchMatches,
	"ListMigrations":     listMigrations,

	"ListReposIn":            listReposIn,
	"ListReposByIDs":         listReposByIDs,
	"ListReposInByPublished": listReposInByPublished,
	"ListLargestReposIn":     listLargestReposIn,
}

// The lists of the repos of a caller's grants (GET /api/repos, GET /api/overview): one rowid
// lookup per id, then sorted. sqlc numbers the parameters that follow a slice wrongly, so these
// take the ids last: idsMark becomes one "?" per id.
const idsMark = "/*IDS*/?"

const repoColumns = `r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id, o.type, o.owner, o.ref, o.state, o.done, o.total, o.error, o.started_by, o.started_at, o.updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id`

// listReposIn: ?1 from, ?2 to, ?3 format ('' any), ?4 kind ('' any), ?5 limit, then the ids. Each
// parameter appears once: the plan guard binds one NULL per "?".
const listReposIn = `SELECT ` + repoColumns + `
WHERE r.name >= ?1 AND r.name < ?2 AND r.format = coalesce(nullif(?3, ''), r.format) AND r.kind = coalesce(nullif(?4, ''), r.kind)
  AND r.id IN (` + idsMark + `)
ORDER BY r.name LIMIT ?5`

// listReposByIDs: the repos of the ids (the members of a group), in no order.
const listReposByIDs = `SELECT ` + repoColumns + `
WHERE r.id IN (` + idsMark + `)`

// listReposInByPublished: ?1 limit, then the ids.
const listReposInByPublished = `SELECT ` + repoColumns + `
WHERE r.id IN (` + idsMark + `) ORDER BY r.last_published_at DESC, r.id DESC LIMIT ?1`

// listLargestReposIn: ?1 limit, then the ids.
const listLargestReposIn = `SELECT ` + repoColumns + `
WHERE r.total_size > 0 AND r.id IN (` + idsMark + `) ORDER BY r.total_size DESC, r.id DESC LIMIT ?1`

// withIDs returns query with one placeholder per id and the arguments: args, then the ids.
func withIDs(query string, ids []int64, args ...any) (string, []any) {
	marks := "NULL"
	if len(ids) > 0 {
		marks = strings.Repeat(",?", len(ids))[1:]
	}
	for _, id := range ids {
		args = append(args, id)
	}
	return strings.Replace(query, idsMark, marks, 1), args
}

// ListReposIn returns the repos of ids with a name in [from, to), of format and kind ('' for any),
// by name, at most limit.
func (q *Queries) ListReposIn(ctx context.Context, ids []int64, from, to, format, kind string, limit int64) ([]GetRepoByNameRow, error) {
	query, args := withIDs(listReposIn, ids, from, to, format, kind, limit)
	return q.repoRows(ctx, query, args)
}

// ListReposByIDs returns the repos of ids that exist, in no order.
func (q *Queries) ListReposByIDs(ctx context.Context, ids []int64) ([]GetRepoByNameRow, error) {
	query, args := withIDs(listReposByIDs, ids)
	return q.repoRows(ctx, query, args)
}

// ListReposInByPublished returns the limit repos of ids published last.
func (q *Queries) ListReposInByPublished(ctx context.Context, ids []int64, limit int64) ([]GetRepoByNameRow, error) {
	query, args := withIDs(listReposInByPublished, ids, limit)
	return q.repoRows(ctx, query, args)
}

// ListLargestReposIn returns the limit repos of ids that store the most bytes; none that stores
// nothing.
func (q *Queries) ListLargestReposIn(ctx context.Context, ids []int64, limit int64) ([]GetRepoByNameRow, error) {
	query, args := withIDs(listLargestReposIn, ids, limit)
	return q.repoRows(ctx, query, args)
}

func (q *Queries) repoRows(ctx context.Context, query string, args []any) ([]GetRepoByNameRow, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GetRepoByNameRow
	for rows.Next() {
		var i GetRepoByNameRow
		if err := rows.Scan(&i.ID, &i.Name, &i.Format, &i.Kind, &i.AnonymousRead, &i.Config, &i.CreatedAt, &i.Description,
			&i.FileCount, &i.TotalSize, &i.LastPublishedAt, &i.OpID, &i.OpType, &i.OpOwner, &i.OpRef, &i.OpState, &i.OpDone,
			&i.OpTotal, &i.OpError, &i.OpStartedBy, &i.OpStartedAt, &i.OpUpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
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
