-- Storage moves (SPEC.md "Storage move"): the move service and its background copier.

-- The target is sealed with the row id as AAD, so the service inserts an empty target and
-- sets it with SetMoveTarget in the same transaction. A second copying move violates the
-- storage_moves_copying unique index.
-- name: CreateMove :one
INSERT INTO storage_moves (target, state, total, started_by, started_at)
VALUES (X'', 'copying', ?, ?, ?)
RETURNING id;

-- name: SetMoveTarget :exec
UPDATE storage_moves SET target = ? WHERE id = ?;

-- name: GetMove :one
SELECT id, target, state, cursor, total, copied, skipped, failed, error, started_by, started_at, finished_at
FROM storage_moves WHERE id = ?;

-- The running move, through the storage_moves_copying partial index.
-- name: GetCopyingMove :one
SELECT id, target, state, cursor, total, copied, skipped, failed, error, started_by, started_at, finished_at
FROM storage_moves WHERE state = 'copying';

-- The newest move, running or finished. max(id) is one probe at the end of the rowid tree.
-- name: GetLatestMove :one
SELECT id, target, state, cursor, total, copied, skipped, failed, error, started_by, started_at, finished_at
FROM storage_moves WHERE id = (SELECT max(id) FROM storage_moves);

-- The copier saves its position and counters after each page. Only a copying move changes,
-- so a page that ends after a cancel writes nothing.
-- name: UpdateMoveProgress :execrows
UPDATE storage_moves SET cursor = ?, copied = ?, skipped = ?, failed = ?
WHERE id = ? AND state = 'copying';

-- Ends a copying move as done, failed or cancelled. Returns 0 rows when it already ended.
-- name: FinishMove :execrows
UPDATE storage_moves SET state = ?, error = ?, finished_at = ?
WHERE id = ? AND state = 'copying';

-- A repeated failure of the same blob (a resumed page) replaces the reason.
-- name: InsertMoveFailure :exec
INSERT INTO storage_move_failures (move_id, blob_id, key, reason) VALUES (?, ?, ?, ?)
ON CONFLICT (move_id, blob_id) DO UPDATE SET key = excluded.key, reason = excluded.reason;

-- Keyset page of a move's failures in blob id order.
-- name: ListMoveFailures :many
SELECT blob_id, key, reason FROM storage_move_failures
WHERE move_id = sqlc.arg(move_id) AND blob_id > sqlc.arg(after_id)
ORDER BY blob_id LIMIT sqlc.arg(limit);

-- The copier's walk: a keyset page of every blob in id order.
-- name: WalkBlobs :many
SELECT id, sha256, size FROM blobs
WHERE id > sqlc.arg(after_id)
ORDER BY id LIMIT sqlc.arg(limit);

-- One probe: whether any blob exists (an empty server switches storage without a move).
-- name: AnyBlob :one
SELECT id FROM blobs WHERE id > 0 ORDER BY id LIMIT 1;

-- The move's total. It reads the whole table once, when an admin starts a move.
-- name: CountBlobs :one
SELECT count(*) FROM blobs;