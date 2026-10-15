-- The assets table (internal/assets). The FTS5 search is hand-written in dbq/extra.go.

-- Indexed point lookup on (repo_id, path), used by every download.
-- name: GetAsset :one
SELECT id, repo_id, path, size, sha256, content_type, kind, uploaded_by, created_at, updated_at, publish_session_id,
  checked_at, remote_etag
FROM assets WHERE repo_id = ? AND path = ?;

-- name: GetAssetID :one
SELECT id FROM assets WHERE repo_id = ? AND path = ?;

-- Path and kind never change on update (the FTS triggers rely on it).
-- name: UpsertAsset :one
INSERT INTO assets (repo_id, path, size, sha256, content_type, kind, uploaded_by, created_at, updated_at, publish_session_id,
  checked_at, remote_etag)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (repo_id, path) DO UPDATE SET size = excluded.size, sha256 = excluded.sha256,
  content_type = excluded.content_type, updated_at = excluded.updated_at, uploaded_by = excluded.uploaded_by,
  publish_session_id = excluded.publish_session_id, checked_at = excluded.checked_at, remote_etag = excluded.remote_etag
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
SELECT id, repo_id, path, size, sha256, content_type, kind, uploaded_by, created_at, updated_at, publish_session_id,
  checked_at, remote_etag
FROM assets
WHERE repo_id = sqlc.arg(repo_id) AND path >= sqlc.arg(lower)
ORDER BY path LIMIT sqlc.arg(limit);

-- name: ListAssetsAfter :many
SELECT id, repo_id, path, size, sha256, content_type, kind, uploaded_by, created_at, updated_at, publish_session_id,
  checked_at, remote_etag
FROM assets
WHERE repo_id = sqlc.arg(repo_id) AND path > sqlc.arg(lower)
ORDER BY path LIMIT sqlc.arg(limit);

-- name: ListAssetsFromRange :many
SELECT id, repo_id, path, size, sha256, content_type, kind, uploaded_by, created_at, updated_at, publish_session_id,
  checked_at, remote_etag
FROM assets
WHERE repo_id = sqlc.arg(repo_id) AND path >= sqlc.arg(lower) AND path < sqlc.arg(upper)
ORDER BY path LIMIT sqlc.arg(limit);

-- name: ListAssetsAfterRange :many
SELECT id, repo_id, path, size, sha256, content_type, kind, uploaded_by, created_at, updated_at, publish_session_id,
  checked_at, remote_etag
FROM assets
WHERE repo_id = sqlc.arg(repo_id) AND path > sqlc.arg(lower) AND path < sqlc.arg(upper)
ORDER BY path LIMIT sqlc.arg(limit);

-- A proxy's remote confirmed the cached file (a 304, or a 404 while the cached copy stays).
-- name: SetAssetChecked :exec
UPDATE assets SET checked_at = ?, remote_etag = ? WHERE repo_id = ? AND path = ?;
