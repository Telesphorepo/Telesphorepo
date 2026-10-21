-- Backups (internal/backup): reads of the live database and of the database copy of a backup.
-- None of them runs on a request path.

-- A keyset page of the blobs that an asset uses, in id order: the content a backup copies and a
-- restore needs.
-- name: WalkReferencedBlobs :many
SELECT id, sha256, size FROM blobs
WHERE id > sqlc.arg(after_id) AND refs > 0
ORDER BY id LIMIT sqlc.arg(limit);

-- The counts that backup.json records: the repos, their files and the bytes of their files.
-- name: CountRepoTotals :one
SELECT count(*) AS repos,
  CAST(coalesce(sum(file_count), 0) AS INTEGER) AS files,
  CAST(coalesce(sum(total_size), 0) AS INTEGER) AS bytes
FROM repos;

-- One stored setting, as its JSON value. An offline backup and a restore read the storage
-- settings of a database this way (a secret setting is sealed, and neither reads one).
-- name: GetSetting :one
SELECT value FROM settings WHERE key = ?;
