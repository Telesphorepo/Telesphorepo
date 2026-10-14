-- Nexus sources, mirroring syncs and cut-over (spec section 12): the Nexus API and the sync
-- runner and scheduler (internal/nexus).

-- The credentials are sealed with the row id as AAD, so the service inserts empty credentials
-- and sets them with SetNexusSourceCredentials in the same transaction.
-- name: CreateNexusSource :one
INSERT INTO nexus_sources (name, base_url, credentials, created_by, created_at)
VALUES (?, ?, X'', ?, ?)
RETURNING id;

-- name: SetNexusSourceCredentials :exec
UPDATE nexus_sources SET base_url = ?, credentials = ? WHERE id = ?;

-- name: GetNexusSource :one
SELECT id, name, base_url, credentials, state, created_by, created_at, last_sync_at, last_error
FROM nexus_sources WHERE id = ?;

-- Keyset page by name (pass "" for the first page).
-- name: ListNexusSources :many
SELECT id, name, base_url, credentials, state, created_by, created_at, last_sync_at, last_error
FROM nexus_sources WHERE name > sqlc.arg(after) ORDER BY name LIMIT sqlc.arg(limit);

-- The mirroring sources whose last sync ended at or before the cutoff, oldest first, through
-- the nexus_sources_due partial index.
-- name: ListDueNexusSources :many
SELECT id FROM nexus_sources
WHERE state = 'mirroring' AND last_sync_at <= sqlc.arg(cutoff)
ORDER BY last_sync_at LIMIT sqlc.arg(limit);

-- name: SetNexusSourceSynced :exec
UPDATE nexus_sources SET last_sync_at = ?, last_error = ? WHERE id = ?;

-- Ends mirroring and drops the credentials. Returns 0 rows when the source already cut over.
-- name: CutOverNexusSource :execrows
UPDATE nexus_sources SET state = 'cut_over', credentials = X'' WHERE id = ? AND state = 'mirroring';

-- Deletes the source with its mapping and its syncs (ON DELETE CASCADE).
-- name: DeleteNexusSource :exec
DELETE FROM nexus_sources WHERE id = ?;

-- A source's mapped repos in source repo order (the primary key order).
-- name: ListNexusSourceRepos :many
SELECT source_id, source_repo, source_format, target_repo_id
FROM nexus_source_repos WHERE source_id = ? ORDER BY source_repo;

-- name: DeleteNexusSourceRepos :exec
DELETE FROM nexus_source_repos WHERE source_id = ?;

-- name: InsertNexusSourceRepo :exec
INSERT INTO nexus_source_repos (source_id, source_repo, source_format, target_repo_id) VALUES (?, ?, ?, ?);

-- The source that a target repo mirrors, through nexus_source_repos_target.
-- name: GetNexusSourceByTarget :one
SELECT s.id, s.name FROM nexus_source_repos m JOIN nexus_sources s ON s.id = m.source_id
WHERE m.target_repo_id = ? LIMIT 1;

-- A second running sync of the same source violates the nexus_syncs_running unique index.
-- name: CreateNexusSync :one
INSERT INTO nexus_syncs (source_id, state, cutover, started_by, started_at)
VALUES (?, 'running', ?, ?, ?)
RETURNING id;

-- Copies the source's mapping into the sync's repos.
-- name: InsertNexusSyncRepos :execrows
INSERT INTO nexus_sync_repos (sync_id, source_repo, source_format, target_repo_id)
SELECT sqlc.arg(sync_id), source_repo, source_format, target_repo_id
FROM nexus_source_repos WHERE source_id = sqlc.arg(source_id);

-- name: GetNexusSync :one
SELECT id, source_id, state, cutover, started_by, started_at, finished_at, error
FROM nexus_syncs WHERE id = ?;

-- The running sync of a source, through the nexus_syncs_running partial index.
-- name: GetRunningNexusSync :one
SELECT id, source_id, state, cutover, started_by, started_at, finished_at, error
FROM nexus_syncs WHERE source_id = ? AND state = 'running';

-- Every running sync (startup resume), a scan of the nexus_syncs_running partial index.
-- name: ListRunningNexusSyncs :many
SELECT id, source_id, state, cutover, started_by, started_at, finished_at, error
FROM nexus_syncs WHERE state = 'running' ORDER BY source_id;

-- name: GetLatestNexusSync :one
SELECT id, source_id, state, cutover, started_by, started_at, finished_at, error
FROM nexus_syncs WHERE source_id = ? ORDER BY id DESC LIMIT 1;

-- Keyset page of a source's syncs, newest first: ids below before.
-- name: ListNexusSyncs :many
SELECT id, source_id, state, cutover, started_by, started_at, finished_at, error
FROM nexus_syncs WHERE source_id = sqlc.arg(source_id) AND id < sqlc.arg(before)
ORDER BY id DESC LIMIT sqlc.arg(limit);

-- Ends a running sync as done, failed or cancelled. Returns 0 rows when it already ended.
-- name: FinishNexusSync :execrows
UPDATE nexus_syncs SET state = ?, error = ?, finished_at = ? WHERE id = ? AND state = 'running';

-- A sync's repos in source repo order (the primary key order).
-- name: ListNexusSyncRepos :many
SELECT sync_id, source_repo, source_format, target_repo_id, continuation, imported, skipped, ignored, failed, state, error
FROM nexus_sync_repos WHERE sync_id = ? ORDER BY source_repo;

-- The runner saves the next page's continuation token with the counters after each page.
-- name: UpdateNexusSyncRepoProgress :exec
UPDATE nexus_sync_repos SET continuation = ?, imported = ?, skipped = ?, ignored = ?, failed = ?
WHERE sync_id = ? AND source_repo = ?;

-- name: SetNexusSyncRepoState :exec
UPDATE nexus_sync_repos SET state = ?, error = ? WHERE sync_id = ? AND source_repo = ?;

-- A repeated failure of the same file (a resumed page) replaces the reason and keeps its id.
-- name: InsertNexusSyncFailure :exec
INSERT INTO nexus_sync_failures (sync_id, source_repo, path, reason) VALUES (?, ?, ?, ?)
ON CONFLICT (sync_id, source_repo, path) DO UPDATE SET reason = excluded.reason;

-- Keyset page of a sync's failures in insertion order.
-- name: ListNexusSyncFailures :many
SELECT id, source_repo, path, reason FROM nexus_sync_failures
WHERE sync_id = sqlc.arg(sync_id) AND id > sqlc.arg(after_id)
ORDER BY id LIMIT sqlc.arg(limit);
