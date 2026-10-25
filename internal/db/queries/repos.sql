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

-- A merge gives every redirect of a repo to the repo it merges into, through repo_redirects_repo.
-- name: MoveRepoRedirects :execrows
UPDATE repo_redirects SET repo_id = sqlc.arg(to_id) WHERE repo_id = sqlc.arg(from_id);

-- name: RenameRepo :exec
UPDATE repos SET name = ? WHERE id = ?;
-- The lists of GET /api/repos (internal/repos list.go). A page by name is the names in
-- [from, to): the keyset cursor and a name prefix in one range of an index (migration
-- 0005_performance.sql). kind '' and format '' match every repo. The lists of the repos of a
-- caller's grants are in dbq/extra.go (sqlc numbers the parameters after a slice wrongly).

-- name: ListReposInRange :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE r.name >= sqlc.arg(from) AND r.name < sqlc.arg(to) ORDER BY r.name LIMIT sqlc.arg(limit);

-- name: ListReposOfFormat :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE r.format = sqlc.arg(format) AND r.name >= sqlc.arg(from) AND r.name < sqlc.arg(to)
  AND (CAST(sqlc.arg(kind) AS TEXT) = '' OR r.kind = sqlc.arg(kind))
ORDER BY r.name LIMIT sqlc.arg(limit);

-- name: ListReposOfKind :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE r.kind = sqlc.arg(kind) AND r.name >= sqlc.arg(from) AND r.name < sqlc.arg(to) ORDER BY r.name LIMIT sqlc.arg(limit);

-- The public repos, through repos_anonymous_name.
-- name: ListAnonymousRepos :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE r.anonymous_read = 1 AND r.name >= sqlc.arg(from) AND r.name < sqlc.arg(to)
  AND (CAST(sqlc.arg(kind) AS TEXT) = '' OR r.kind = sqlc.arg(kind))
ORDER BY r.name LIMIT sqlc.arg(limit);

-- name: ListAnonymousReposOfFormat :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE r.anonymous_read = 1 AND r.format = sqlc.arg(format) AND r.name >= sqlc.arg(from) AND r.name < sqlc.arg(to)
  AND (CAST(sqlc.arg(kind) AS TEXT) = '' OR r.kind = sqlc.arg(kind))
ORDER BY r.name LIMIT sqlc.arg(limit);

-- The newest publishes first, through repos_published.
-- name: ListReposByPublished :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id
ORDER BY r.last_published_at DESC, r.id DESC LIMIT sqlc.arg(limit);

-- name: ListAnonymousReposByPublished :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE r.anonymous_read = 1 ORDER BY r.last_published_at DESC, r.id DESC LIMIT sqlc.arg(limit);

-- The repos of the names, through the UNIQUE name (at most a page of names).
-- name: ListReposNamed :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE r.name IN (sqlc.slice(names));

-- The largest repos that store something, through repos_size.
-- name: ListLargestRepos :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repos r LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE r.total_size > 0 ORDER BY r.total_size DESC, r.id DESC LIMIT sqlc.arg(limit);

-- The totals of every repo (repo_totals, kept by triggers).
-- name: GetRepoTotals :one
SELECT repos, files, bytes, public FROM repo_totals WHERE id = 1;

-- The totals of the repos of a caller's grants.
-- name: SumReposIn :one
SELECT count(*) AS repos, CAST(coalesce(sum(file_count), 0) AS INTEGER) AS files,
  CAST(coalesce(sum(total_size), 0) AS INTEGER) AS bytes
FROM repos WHERE id IN (sqlc.slice(ids));

