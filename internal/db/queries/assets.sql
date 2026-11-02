-- The assets table (internal/assets). Only the asset pipeline writes it. The FTS5 search is
-- hand-written in dbq/extra.go.

-- Indexed point lookup on (repo_id, path), used by every download.
-- name: GetAsset :one
SELECT id, repo_id, path, blob_id, size, sha256, content_type, kind, uploaded_by, created_at, updated_at,
  build_id, checked_at, remote_etag, md5, sha1, unit
FROM assets WHERE repo_id = ? AND path = ?;

-- Path and kind never change on update (the FTS triggers rely on it). created_at changes only on
-- insert.
-- name: UpsertAsset :one
INSERT INTO assets (repo_id, path, blob_id, size, sha256, content_type, kind, uploaded_by, created_at, updated_at,
  build_id, checked_at, remote_etag, md5, sha1, unit)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (repo_id, path) DO UPDATE SET blob_id = excluded.blob_id, size = excluded.size, sha256 = excluded.sha256,
  content_type = excluded.content_type, updated_at = excluded.updated_at, uploaded_by = excluded.uploaded_by,
  build_id = excluded.build_id, checked_at = excluded.checked_at, remote_etag = excluded.remote_etag,
  md5 = excluded.md5, sha1 = excluded.sha1
RETURNING id, kind, created_at, updated_at;

-- An import gives a file the dates of its source (a file that a sync skipped because its bytes
-- are here already).
-- name: SetAssetTimes :exec
UPDATE assets SET created_at = ?, updated_at = ? WHERE repo_id = ? AND path = ?;

-- name: DeleteAsset :execrows
DELETE FROM assets WHERE repo_id = ? AND path = ?;

-- Keyset pages of a repo's assets, ordered by path. Each query has exactly one lower bound on
-- path: SQLite's range scan uses one lower bound per index column and checks any other bound row
-- by row, which would make deep pages scan from the start of the repo. The caller passes the
-- tighter of the prefix and the cursor. The *Range forms add the prefix's upper bound.
-- name: ListAssetsFrom :many
SELECT id, repo_id, path, blob_id, size, sha256, content_type, kind, uploaded_by, created_at, updated_at,
  build_id, checked_at, remote_etag, md5, sha1, unit
FROM assets
WHERE repo_id = sqlc.arg(repo_id) AND path >= sqlc.arg(lower)
ORDER BY path LIMIT sqlc.arg(limit);

-- name: ListAssetsAfter :many
SELECT id, repo_id, path, blob_id, size, sha256, content_type, kind, uploaded_by, created_at, updated_at,
  build_id, checked_at, remote_etag, md5, sha1, unit
FROM assets
WHERE repo_id = sqlc.arg(repo_id) AND path > sqlc.arg(lower)
ORDER BY path LIMIT sqlc.arg(limit);

-- name: ListAssetsFromRange :many
SELECT id, repo_id, path, blob_id, size, sha256, content_type, kind, uploaded_by, created_at, updated_at,
  build_id, checked_at, remote_etag, md5, sha1, unit
FROM assets
WHERE repo_id = sqlc.arg(repo_id) AND path >= sqlc.arg(lower) AND path < sqlc.arg(upper)
ORDER BY path LIMIT sqlc.arg(limit);

-- name: ListAssetsAfterRange :many
SELECT id, repo_id, path, blob_id, size, sha256, content_type, kind, uploaded_by, created_at, updated_at,
  build_id, checked_at, remote_etag, md5, sha1, unit
FROM assets
WHERE repo_id = sqlc.arg(repo_id) AND path > sqlc.arg(lower) AND path < sqlc.arg(upper)
ORDER BY path LIMIT sqlc.arg(limit);

-- The assets of one repo that point to one blob, through assets_blob: e.g. the tags of a
-- manifest digest.
-- name: ListAssetsByBlob :many
SELECT id, repo_id, path, blob_id, size, sha256, content_type, kind, uploaded_by, created_at, updated_at,
  build_id, checked_at, remote_etag, md5, sha1, unit
FROM assets
WHERE blob_id = ? AND repo_id = ?
LIMIT ?;

-- A proxy's remote confirmed the cached file (a 304, or a 404 while the cached copy stays).
-- name: SetAssetChecked :exec
UPDATE assets SET checked_at = ?, remote_etag = ? WHERE repo_id = ? AND path = ?;

-- The children of each folder (tree_entries), written with each asset insert and delete. A file
-- row is new when AddTreeFile inserts it; only then do its folders count one more file.
-- name: AddTreeFile :execrows
INSERT INTO tree_entries (repo_id, parent, kind, sort_key, name, files, asset_id)
VALUES (?, ?, 1, ?, ?, 0, ?)
ON CONFLICT (repo_id, parent, kind, sort_key) DO NOTHING;

-- name: DeleteTreeFile :execrows
DELETE FROM tree_entries WHERE repo_id = ? AND parent = ? AND kind = 1 AND sort_key = ?;

-- name: AddTreeFolderFile :exec
INSERT INTO tree_entries (repo_id, parent, kind, sort_key, name, files)
VALUES (?, ?, 0, ?, ?, 1)
ON CONFLICT (repo_id, parent, kind, sort_key) DO UPDATE SET files = files + 1;

-- name: DropTreeFolderFile :one
UPDATE tree_entries SET files = files - 1
WHERE repo_id = ? AND parent = ? AND kind = 0 AND sort_key = ?
RETURNING files;

-- name: DeleteTreeFolder :exec
DELETE FROM tree_entries WHERE repo_id = ? AND parent = ? AND kind = 0 AND sort_key = ?;

-- Pages of the children of a folder: one range of the primary key each. A cursor's key with a 0
-- byte appended is the smallest key after it.
-- name: ListTreeFolders :many
SELECT sort_key, name FROM tree_entries
WHERE repo_id = sqlc.arg(repo_id) AND parent = sqlc.arg(parent) AND kind = 0 AND sort_key >= sqlc.arg(lower)
ORDER BY sort_key LIMIT sqlc.arg(limit);

-- name: ListTreeFoldersDesc :many
SELECT sort_key, name FROM tree_entries
WHERE repo_id = sqlc.arg(repo_id) AND parent = sqlc.arg(parent) AND kind = 0 AND sort_key < sqlc.arg(upper)
ORDER BY sort_key DESC LIMIT sqlc.arg(limit);

-- name: ListTreeFiles :many
SELECT t.sort_key, a.id, a.path, a.size, a.sha256, a.uploaded_by, a.updated_at, a.build_id
FROM tree_entries t JOIN assets a ON a.id = t.asset_id
WHERE t.repo_id = sqlc.arg(repo_id) AND t.parent = sqlc.arg(parent) AND t.kind = 1 AND t.sort_key >= sqlc.arg(lower)
ORDER BY t.sort_key LIMIT sqlc.arg(limit);

-- name: ListTreeFilesDesc :many
SELECT t.sort_key, a.id, a.path, a.size, a.sha256, a.uploaded_by, a.updated_at, a.build_id
FROM tree_entries t JOIN assets a ON a.id = t.asset_id
WHERE t.repo_id = sqlc.arg(repo_id) AND t.parent = sqlc.arg(parent) AND t.kind = 1 AND t.sort_key < sqlc.arg(upper)
ORDER BY t.sort_key DESC LIMIT sqlc.arg(limit);

-- The last key of a kind of children of a folder: one seek.
-- name: LastTreeKey :one
SELECT sort_key FROM tree_entries
WHERE repo_id = sqlc.arg(repo_id) AND parent = sqlc.arg(parent) AND kind = sqlc.arg(kind)
ORDER BY sort_key DESC LIMIT 1;

-- name: HasTreeFile :one
SELECT EXISTS (SELECT 1 FROM tree_entries WHERE repo_id = ? AND parent = ? AND kind = 1 AND sort_key = ?);

-- Content-addressed blobs (internal/blobs).

-- name: GetBlob :one
SELECT id, sha256, size, refs, md5, sha1 FROM blobs WHERE sha256 = ?;

-- A new blob has no reference yet: it counts as unreferenced since it was created, so a blob
-- whose commit failed reaches the GC after the grace.
-- name: InsertBlob :one
INSERT INTO blobs (sha256, size, refs, unreferenced_at, created_at, md5, sha1) VALUES (?, ?, 0, ?, ?, ?, ?)
RETURNING id;

-- The blobs that no asset referenced since before cutoff, oldest first, through
-- blobs_unreferenced.
-- name: ListUnreferencedBlobs :many
SELECT id, sha256 FROM blobs
WHERE refs = 0 AND unreferenced_at < sqlc.arg(cutoff)
ORDER BY unreferenced_at LIMIT sqlc.arg(limit);

-- The blobs of ids that no asset referenced since before cutoff (the GC checks a round under its
-- digest locks). The slice comes last: sqlc writes the cutoff as ?2 after a slice, and SQLite
-- gives ?2 to the second id of the expanded slice.
-- name: ListCollectableBlobs :many
SELECT id FROM blobs WHERE refs = 0 AND unreferenced_at < sqlc.arg(cutoff) AND id IN (sqlc.slice(ids));

-- Deletes a blob row only while no asset points to it.
-- name: DeleteUnreferencedBlob :execrows
DELETE FROM blobs WHERE id = ? AND refs = 0;

-- Resumable uploads (assets.Uploads).

-- name: InsertUploadSession :exec
INSERT INTO upload_sessions (id, repo_id, user_id, publish_session_id, received, created_at, updated_at, expires_at)
VALUES (?, ?, ?, ?, 0, ?, ?, ?);

-- name: GetUploadSession :one
SELECT id, repo_id, user_id, publish_session_id, received, created_at, updated_at, expires_at
FROM upload_sessions WHERE id = ?;

-- name: SetUploadReceived :exec
UPDATE upload_sessions SET received = ?, updated_at = ?, expires_at = ? WHERE id = ?;

-- name: SetUploadExpiry :exec
UPDATE upload_sessions SET expires_at = ? WHERE id = ?;

-- name: DeleteUploadSession :exec
DELETE FROM upload_sessions WHERE id = ?;

-- The uploads idle past their expiry, through upload_sessions_expires.
-- name: ListExpiredUploads :many
SELECT id FROM upload_sessions WHERE expires_at < sqlc.arg(now) ORDER BY expires_at LIMIT sqlc.arg(limit);
