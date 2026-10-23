-- Repository records (internal/repos).

-- A repo with its active operation (repo_ops), if any. Indexed point lookup, used on every request.
-- name: GetRepoByName :one
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE r.name = ?;

-- name: GetRepoByID :one
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE r.id = ?;

-- Keyset page by name (pass "" for the first page).
-- name: ListRepos :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE r.name > sqlc.arg(after) ORDER BY r.name LIMIT sqlc.arg(limit);

-- name: CreateRepo :one
INSERT INTO repos (name, format, kind, anonymous_read, config, description, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: UpdateRepo :exec
UPDATE repos SET config = ?, description = ?, anonymous_read = ? WHERE id = ?;


-- Deletes the repo; its remaining rows cascade.
-- name: DeleteRepo :execrows
DELETE FROM repos WHERE id = ?;

-- Group members. The repos service keeps them in memory: it reads them group by group at start
-- and after a change.

-- The next group with members after group id after (0 for the first).
-- name: NextGroupWithMembers :one
SELECT group_id FROM repo_group_members WHERE group_id > ? ORDER BY group_id LIMIT 1;

-- name: ListGroupMembers :many
SELECT member_id FROM repo_group_members WHERE group_id = ? ORDER BY position;

-- name: DeleteGroupMembers :exec
DELETE FROM repo_group_members WHERE group_id = ?;

-- name: InsertGroupMember :exec
INSERT INTO repo_group_members (group_id, member_id, position) VALUES (?, ?, ?);

-- Proxy remotes. The repos service keeps them in memory; it reads them in pages of repo ids at
-- start.

-- name: ListProxies :many
SELECT repo_id, remote_url, credentials, metadata_ttl, not_found_ttl, last_ok_at, last_error, last_error_at
FROM repo_proxies WHERE repo_id > sqlc.arg(after) ORDER BY repo_id LIMIT sqlc.arg(limit);

-- name: InsertProxy :exec
INSERT INTO repo_proxies (repo_id, remote_url, credentials, metadata_ttl, not_found_ttl) VALUES (?, ?, ?, ?, ?);

-- name: UpdateProxy :exec
UPDATE repo_proxies SET remote_url = ?, credentials = ?, metadata_ttl = ?, not_found_ttl = ? WHERE repo_id = ?;

-- name: SetProxyOK :exec
UPDATE repo_proxies SET last_ok_at = ? WHERE repo_id = ?;

-- name: SetProxyError :exec
UPDATE repo_proxies SET last_error = ?, last_error_at = ? WHERE repo_id = ?;

-- Repo operations (internal/ops).

-- name: InsertRepoOp :one
INSERT INTO repo_ops (repo_id, type, owner, ref, state, started_by, started_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: GetRepoOp :one
SELECT id, repo_id, type, owner, ref, state, done, total, error, started_by, started_at, updated_at
FROM repo_ops WHERE repo_id = ?;

-- name: SetRepoOpProgress :exec
UPDATE repo_ops SET done = ?, total = ?, updated_at = ? WHERE id = ?;

-- name: SetRepoOpState :exec
UPDATE repo_ops SET state = ?, error = ?, updated_at = ? WHERE id = ?;

-- name: DeleteRepoOp :execrows
DELETE FROM repo_ops WHERE id = ? AND owner = ?;

-- The running operations of one type, through repo_ops_running (resumed at startup).
-- name: ListRunningRepoOps :many
SELECT id, repo_id, type, owner, ref, state, done, total, error, started_by, started_at, updated_at
FROM repo_ops WHERE type = ? AND state = 'running' LIMIT ?;

-- The rebuild queue (internal/rebuild).

-- name: MarkRebuild :exec
INSERT INTO rebuilds (repo_id, grp, unit, marked_at) VALUES (?, ?, ?, ?)
ON CONFLICT (repo_id, grp, unit) DO UPDATE SET marked_at = excluded.marked_at;

-- name: CountRebuilds :one
SELECT count(*) FROM rebuilds WHERE repo_id = ?;

-- name: ListRebuildUnits :many
SELECT unit FROM rebuilds WHERE repo_id = ? AND grp = ? ORDER BY unit;

-- Deletes a unit unless it was marked again after the rebuild started.
-- name: ClearRebuild :exec
DELETE FROM rebuilds WHERE repo_id = ? AND grp = ? AND unit = ? AND marked_at <= ?;

-- name: HasRebuild :one
SELECT EXISTS (SELECT 1 FROM rebuilds WHERE repo_id = ? AND grp = ?);

-- Startup: every (repo, grp) with pending units.
-- name: ListRebuildGroups :many
SELECT DISTINCT repo_id, grp FROM rebuilds ORDER BY repo_id, grp;

-- Startup: the newest mark, so the rebuild clock never goes back.
-- name: MaxRebuildMark :one
SELECT CAST(COALESCE(MAX(marked_at), 0) AS INTEGER) AS max_mark FROM rebuilds;
-- Repository redirects (internal/repos): a name that answers for a repo. Resolving a name reads
-- repos first (GetRepoByName), then this point lookup by the key.
-- name: GetRepoRedirect :one
SELECT name, repo_id, created_at, last_used_at FROM repo_redirects WHERE name = ?;

-- The redirects of one repo by name, through repo_redirects_repo.
-- name: ListRepoRedirectsOf :many
SELECT name, repo_id, created_at, last_used_at FROM repo_redirects WHERE repo_id = ? ORDER BY name;

-- Keyset page of every redirect by name (the edge reconcile).
-- name: ListRepoRedirects :many
SELECT name, repo_id FROM repo_redirects WHERE name > sqlc.arg(after) ORDER BY name LIMIT sqlc.arg(limit);

-- name: InsertRepoRedirect :exec
INSERT INTO repo_redirects (name, repo_id, created_at) VALUES (?, ?, ?);

-- name: DeleteRepoRedirect :execrows
DELETE FROM repo_redirects WHERE name = ? AND repo_id = ?;

-- name: RenameRepo :exec
UPDATE repos SET name = ? WHERE id = ?;