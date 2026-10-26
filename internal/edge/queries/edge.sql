-- The edge module (internal/edge). The outbox, the written access objects and the state are the
-- module's tables; the reads of users, grants, read tokens, devices and assets feed the access
-- objects and the backfill. None of them runs on a request path, except the status route that
-- reads edge_state by key.

-- The outbox ------------------------------------------------------------------------------------

-- A new dirty row, or the replacement of the row of the key. A put that timed out stays known.
-- name: UpsertOutbox :exec
INSERT INTO edge_outbox (repo_id, path, sha256, due_at) VALUES (?, ?, ?, ?)
ON CONFLICT (repo_id, path) DO UPDATE SET sha256 = excluded.sha256, due_at = excluded.due_at,
  attempts = 0, seq = edge_outbox.seq + 1, error = '';

-- A backfill row: a key that is dirty already keeps its row, which the backfill marks.
-- name: InsertOutbox :exec
INSERT INTO edge_outbox (repo_id, path, sha256, due_at, backfill) VALUES (?, ?, ?, ?, 1)
ON CONFLICT (repo_id, path) DO UPDATE SET backfill = 1;

-- Whether a row of the backfill is not written yet, through edge_outbox_backfill.
-- name: BackfillPending :one
SELECT EXISTS (SELECT 1 FROM edge_outbox WHERE backfill = 1) AS pending;

-- The pointer rows due at now, oldest first, through edge_outbox_due.
-- name: DueOutbox :many
SELECT repo_id, path, sha256, due_at, attempts, seq, timed_out_at FROM edge_outbox
WHERE due_at <= sqlc.arg(now) AND repo_id > 0 ORDER BY due_at LIMIT sqlc.arg(limit);

-- The rows of the access objects and config.json due at now, oldest first, through
-- edge_outbox_access: the worker writes them before the pointers.
-- name: DueAccess :many
SELECT repo_id, path, sha256, due_at, attempts, seq, timed_out_at FROM edge_outbox
WHERE repo_id = 0 AND due_at <= sqlc.arg(now) ORDER BY due_at LIMIT sqlc.arg(limit);

-- name: DeleteOutbox :execrows
DELETE FROM edge_outbox WHERE repo_id = ? AND path = ? AND seq = ?;

-- The row of a removed path whose pointers Removing deleted in every bucket of the current view.
-- A row with a put of unknown outcome stays, for its second application.
-- name: DropRemoved :exec
DELETE FROM edge_outbox WHERE repo_id = ? AND path = ? AND timed_out_at = 0;

-- A failed attempt: the next try and the error.
-- name: RetryOutbox :execrows
UPDATE edge_outbox SET due_at = ?, attempts = attempts + 1, error = ?, timed_out_at = ?
WHERE repo_id = ? AND path = ? AND seq = ?;

-- A later try without a failure: the one-write-per-second spacing of a key, or the second
-- application one minute after a put that timed out.
-- name: DelayOutbox :execrows
UPDATE edge_outbox SET due_at = ?, timed_out_at = ? WHERE repo_id = ? AND path = ? AND seq = ?;

-- Every row, when edge delivery stops (through the primary key).
-- name: ClearOutbox :exec
DELETE FROM edge_outbox WHERE repo_id >= 0;

-- The counts of the status page, while it is open.
-- name: CountOutbox :one
SELECT count(*) AS total, CAST(coalesce(sum(repo_id > 0), 0) AS INTEGER) AS pointers,
  CAST(coalesce(max(CASE WHEN attempts > 0 THEN error END), '') AS TEXT) AS last_error
FROM edge_outbox;

-- The written access objects ---------------------------------------------------------------------

-- name: GetWritten :one
SELECT hash FROM edge_written WHERE key = ?;

-- name: UpsertWritten :exec
INSERT INTO edge_written (key, hash, written_at) VALUES (?, ?, ?)
ON CONFLICT (key) DO UPDATE SET hash = excluded.hash, written_at = excluded.written_at;

-- name: DeleteWritten :exec
DELETE FROM edge_written WHERE key = ?;

-- A keyset page of the keys in (after, upper), through the primary key.
-- name: ListWritten :many
SELECT key, hash FROM edge_written WHERE key > sqlc.arg(after) AND key < sqlc.arg(upper)
ORDER BY key LIMIT sqlc.arg(limit);

-- name: ClearWritten :exec
DELETE FROM edge_written WHERE key >= '';

-- The state -------------------------------------------------------------------------------------

-- name: GetState :one
SELECT value FROM edge_state WHERE key = ?;

-- name: PutState :exec
INSERT INTO edge_state (key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value;

-- name: DeleteState :exec
DELETE FROM edge_state WHERE key = ?;

-- The reads of the access objects ---------------------------------------------------------------

-- name: ListEdgeUsers :many
SELECT id, disabled, kind FROM users WHERE id > ? ORDER BY id LIMIT ?;

-- name: GetEdgeUser :one
SELECT id, disabled, kind FROM users WHERE id = ?;

-- The server permissions of a user, through the user_permissions primary key.
-- name: ListEdgeUserPerms :many
SELECT perm FROM user_permissions WHERE user_id = ?;

-- The repos where a user holds perm, through repo_permissions_user.
-- name: ListEdgeUserGrants :many
SELECT repo_id FROM repo_permissions WHERE user_id = ? AND perm = ?;

-- The server permissions of the users in (after, last], through the user_permissions primary
-- key: the reconcile reads them per page of users.
-- name: ListEdgePermsIn :many
SELECT user_id, perm FROM user_permissions WHERE user_id > sqlc.arg(after) AND user_id <= sqlc.arg(last);

-- The repos where the users in (after, last] hold perm, through repo_permissions_user.
-- name: ListEdgeGrantsIn :many
SELECT user_id, repo_id FROM repo_permissions
WHERE user_id > sqlc.arg(after) AND user_id <= sqlc.arg(last) AND perm = sqlc.arg(perm);

-- name: ListEdgeTokens :many
SELECT id, user_id, public_id, secret_hash, expires_at FROM api_tokens WHERE id > ? ORDER BY id LIMIT ?;

-- name: GetEdgeToken :one
SELECT id, user_id, public_id, secret_hash, expires_at FROM api_tokens WHERE public_id = ?;

-- name: ListEdgeDevices :many
SELECT id, user_id, expires_at, revoked_at FROM devices WHERE id > ? ORDER BY id LIMIT ?;

-- name: GetEdgeDevice :one
SELECT id, user_id, expires_at, revoked_at FROM devices WHERE id = ?;

-- The backfill ----------------------------------------------------------------------------------

-- A keyset page of every asset in id order.
-- name: ListEdgeAssets :many
SELECT id, repo_id, path, sha256 FROM assets WHERE id > ? ORDER BY id LIMIT ?;

-- name: SumFileCount :one
SELECT CAST(coalesce(sum(file_count), 0) AS INTEGER) FROM repos;

-- The path classes of a repo --------------------------------------------------------------------

-- The rule lists of a repo's regional pointers, through the primary key.
-- name: ListEdgePaths :many
SELECT rules, epoch FROM edge_paths WHERE repo_id = ?;

-- name: UpsertEdgePaths :exec
INSERT INTO edge_paths (repo_id, rules, epoch) VALUES (?, ?, ?)
ON CONFLICT (repo_id, rules) DO UPDATE SET epoch = excluded.epoch;

-- The rule lists that no write used since the walk of epoch began.
-- name: PruneEdgePaths :exec
DELETE FROM edge_paths WHERE repo_id = ? AND epoch < ?;

-- name: DeleteEdgePaths :exec
DELETE FROM edge_paths WHERE repo_id = ?;

-- name: GetReclass :one
SELECT epoch, filled, walking, cursor FROM edge_reclass WHERE repo_id = ?;

-- name: PutReclass :exec
INSERT INTO edge_reclass (repo_id, epoch, filled, walking, cursor) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (repo_id) DO UPDATE SET epoch = excluded.epoch, filled = excluded.filled,
  walking = excluded.walking, cursor = excluded.cursor;

-- name: DeleteReclass :exec
DELETE FROM edge_reclass WHERE repo_id = ?;

-- Every row, at a first start (through the primary keys).
-- name: ClearEdgePaths :exec
DELETE FROM edge_paths WHERE repo_id >= 0;

-- name: ClearReclass :exec
DELETE FROM edge_reclass WHERE repo_id >= 0;

-- Whether a pointer row of the repo has a put of unknown outcome, through the primary key: a walk
-- ends only without one, since such a put may still land.
-- name: RepoTimedOut :one
SELECT EXISTS (SELECT 1 FROM edge_outbox WHERE repo_id = ? AND timed_out_at > 0) AS timed_out;
