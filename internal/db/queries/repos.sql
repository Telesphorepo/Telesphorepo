-- Repository records (internal/repos).

-- Indexed point lookup, used on every request.
-- name: GetRepoByName :one
SELECT id, name, format, kind, anonymous_read, config, created_at, locked_by, description,
  file_count, total_size, last_published_at FROM repos WHERE name = ?;

-- name: GetRepoByID :one
SELECT id, name, format, kind, anonymous_read, config, created_at, locked_by, description,
  file_count, total_size, last_published_at FROM repos WHERE id = ?;

-- Keyset page by name (pass "" for the first page).
-- name: ListRepos :many
SELECT id, name, format, kind, anonymous_read, config, created_at, locked_by, description,
  file_count, total_size, last_published_at FROM repos
WHERE name > sqlc.arg(after) ORDER BY name LIMIT sqlc.arg(limit);

-- name: CreateRepo :one
INSERT INTO repos (name, format, kind, anonymous_read, config, description, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: UpdateRepo :exec
UPDATE repos SET anonymous_read = ?, config = ?, description = ? WHERE id = ?;

-- NULL clears the lock.
-- name: SetRepoLockedBy :exec
UPDATE repos SET locked_by = ? WHERE id = ?;

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
