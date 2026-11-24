package dbq

// Hand-written queries that sqlc cannot express. Each one is a named constant in
// Extra, so the plan guard (db.CheckPlans) checks it like a generated query.

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

// Extra maps each hand-written query name to its SQL.
var Extra = map[string]string{
	"NewestBuilds":    newestBuilds,
	"ListFilesUnder":  listFilesUnder,
	"ListVersionsIn":  listVersionsIn,
	"ListBuildsOfAll": listBuildsOfAll,

	"DeleteBuilds":        deleteBuilds,
	"DropVersionBuilds":   dropVersionBuilds,
	"DeleteEmptyVersions": deleteEmptyVersions,
}

// The newest build of each version folder of the JSON array ?2: one seek of the maven_builds
// primary key per folder.
const newestBuilds = `SELECT j.value,
  (SELECT b.build FROM maven_builds b WHERE b.repo_id = ?1 AND b.folder = j.value ORDER BY b.sort_key DESC LIMIT 1)
FROM json_each(?2) j`

// NewestBuilds returns the newest build of each version folder of folders that has one, by
// folder.
func (q *Queries) NewestBuilds(ctx context.Context, repoID int64, folders []string) (map[string]string, error) {
	out := make(map[string]string, len(folders))
	if len(folders) == 0 {
		return out, nil
	}
	js, err := json.Marshal(folders)
	if err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, newestBuilds, repoID, string(js))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var folder string
		var build sql.NullString
		if err := rows.Scan(&folder, &build); err != nil {
			return nil, err
		}
		if build.Valid {
			out[folder] = build.String
		}
	}
	return out, rows.Err()
}

// The files of a repo whose path starts with a prefix of the JSON array ?2: one range read of
// the assets (repo_id, path) key per prefix, [prefix, prefix || 0xFF) (no UTF-8 text holds the
// byte 0xFF). The CROSS JOIN keeps the prefixes the outer loop. A path under two prefixes comes
// twice.
const listFilesUnder = `SELECT a.path, a.size, a.sha256, a.updated_at, a.build_id
FROM json_each(?2) j
CROSS JOIN assets a ON a.repo_id = ?1 AND a.path >= j.value AND a.path < j.value || x'ff'`

// RangeFile is one file of ListFilesUnder.
type RangeFile struct {
	Path      string
	Size      int64
	Sha256    string
	UpdatedAt int64
	BuildID   int64 // 0: none
}

// ListFilesUnder returns the files of repoID whose path starts with one of prefixes, in path
// order under a prefix.
func (q *Queries) ListFilesUnder(ctx context.Context, repoID int64, prefixes []string) ([]RangeFile, error) {
	if len(prefixes) == 0 {
		return nil, nil
	}
	js, err := json.Marshal(prefixes)
	if err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, listFilesUnder, repoID, string(js))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RangeFile
	for rows.Next() {
		var f RangeFile
		var build sql.NullInt64
		if err := rows.Scan(&f.Path, &f.Size, &f.Sha256, &f.UpdatedAt, &build); err != nil {
			return nil, err
		}
		f.BuildID = build.Int64
		out = append(out, f)
	}
	return out, rows.Err()
}

// The versions of an artifact folder whose sort keys are in the JSON array ?3 (hex), through the
// primary key.
const listVersionsIn = `SELECT version, builds FROM maven_versions
WHERE repo_id = ?1 AND folder = ?2 AND sort_key IN (SELECT unhex(value) FROM json_each(?3))`

// VersionRow is one version of ListVersionsIn.
type VersionRow struct {
	Version string
	Builds  int64
}

// ListVersionsIn returns the versions of artifact folder folder of repoID whose sort keys are
// keys, in no order.
func (q *Queries) ListVersionsIn(ctx context.Context, repoID int64, folder string, keys [][]byte) ([]VersionRow, error) {
	hexes := make([]string, len(keys))
	for i, k := range keys {
		hexes[i] = hex.EncodeToString(k)
	}
	js, err := json.Marshal(hexes)
	if err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, listVersionsIn, repoID, folder, string(js))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VersionRow
	for rows.Next() {
		var r VersionRow
		if err := rows.Scan(&r.Version, &r.Builds); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Every build of each version folder of the JSON array ?2, through the primary key. The CROSS
// JOIN keeps the folders the outer loop.
const listBuildsOfAll = `SELECT b.folder, b.build FROM json_each(?2) j
CROSS JOIN maven_builds b ON b.repo_id = ?1 AND b.folder = j.value`

// ListBuildsOfAll returns every build of each version folder of folders, by folder, in no order.
func (q *Queries) ListBuildsOfAll(ctx context.Context, repoID int64, folders []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(folders) == 0 {
		return out, nil
	}
	js, err := json.Marshal(folders)
	if err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, listBuildsOfAll, repoID, string(js))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var folder, build string
		if err := rows.Scan(&folder, &build); err != nil {
			return nil, err
		}
		out[folder] = append(out[folder], build)
	}
	return out, rows.Err()
}

// keysOf is the rows of the JSON array ?2 of [folder, hex of the sort key, ...] of the repo ?1, for
// a row-value IN on a primary key of (repo_id, folder, sort_key).
const keysOf = `SELECT CAST(?1 AS INTEGER), json_extract(j.value, '$[0]'), unhex(json_extract(j.value, '$[1]')) FROM json_each(?2) j`

// The builds of the JSON array ?2 of [version folder, hex of the build key] that lost their last
// file in a remove batch, through the primary key; it returns the ones that were there.
const deleteBuilds = `DELETE FROM maven_builds WHERE (repo_id, folder, sort_key) IN (` + keysOf + `)
RETURNING folder, sort_key`

// Takes the builds of the JSON array ?2 of [artifact folder, hex of the version key, builds
// deleted] off their versions, through the primary key.
const dropVersionBuilds = `UPDATE maven_versions SET builds = builds - (SELECT json_extract(j.value, '$[2]') FROM json_each(?2) j
  WHERE json_extract(j.value, '$[0]') = maven_versions.folder AND unhex(json_extract(j.value, '$[1]')) = maven_versions.sort_key)
WHERE (repo_id, folder, sort_key) IN (` + keysOf + `)`

// Deletes the versions of the JSON array ?2 of [artifact folder, hex of the version key, ...] that
// have no build left, through the primary key.
const deleteEmptyVersions = `DELETE FROM maven_versions WHERE (repo_id, folder, sort_key) IN (` + keysOf + `) AND builds <= 0`

// Key is a row of maven_builds or maven_versions of a repo: its folder and its sort key.
type Key struct {
	Folder  string
	SortKey []byte
}

// keysJSON is keys as the JSON of keysOf, each with its count of counts (when given).
func keysJSON(keys []Key, counts []int64) (string, error) {
	rows := make([][]any, len(keys))
	for i, k := range keys {
		rows[i] = []any{k.Folder, hex.EncodeToString(k.SortKey)}
		if counts != nil {
			rows[i] = append(rows[i], counts[i])
		}
	}
	b, err := json.Marshal(rows)
	return string(b), err
}

// DeleteBuilds deletes the builds keys of repoID and returns the ones that were there.
func (q *Queries) DeleteBuilds(ctx context.Context, repoID int64, keys []Key) ([]Key, error) {
	js, err := keysJSON(keys, nil)
	if err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, deleteBuilds, repoID, js)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var k Key
		if err := rows.Scan(&k.Folder, &k.SortKey); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DropVersionBuilds takes counts[i] builds off version keys[i] of repoID, then deletes the
// versions of keys that have none left.
func (q *Queries) DropVersionBuilds(ctx context.Context, repoID int64, keys []Key, counts []int64) error {
	js, err := keysJSON(keys, counts)
	if err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, dropVersionBuilds, repoID, js); err != nil {
		return err
	}
	_, err = q.db.ExecContext(ctx, deleteEmptyVersions, repoID, js)
	return err
}
