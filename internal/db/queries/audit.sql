-- The audit log (internal/audit).

-- name: InsertAuditEvent :exec
INSERT INTO audit_events (at, actor_user_id, actor, action, repo_id, target, detail, ip)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- Keyset pages newest first: ids in [since, before). A time range of the Activity filter is an id
-- range (FirstAuditAt), so every filter keeps its keyset order. Each filter has its own index;
-- audit_events_actor (actor_user_id) holds the id as its rowid, so it serves (actor_user_id, id).
-- name: ListAudit :many
SELECT id, at, actor_user_id, actor, action, repo_id, target, detail, ip FROM audit_events
WHERE id < sqlc.arg(before) AND id >= sqlc.arg(since) ORDER BY id DESC LIMIT sqlc.arg(limit);

-- name: ListAuditByAction :many
SELECT id, at, actor_user_id, actor, action, repo_id, target, detail, ip FROM audit_events
WHERE action = sqlc.arg(action) AND id < sqlc.arg(before) AND id >= sqlc.arg(since) ORDER BY id DESC LIMIT sqlc.arg(limit);

-- name: ListAuditByRepo :many
SELECT id, at, actor_user_id, actor, action, repo_id, target, detail, ip FROM audit_events
WHERE repo_id = sqlc.arg(repo_id) AND id < sqlc.arg(before) AND id >= sqlc.arg(since) ORDER BY id DESC LIMIT sqlc.arg(limit);

-- audit_events_repo_action serves the repo and the action together.
-- name: ListAuditByRepoAction :many
SELECT id, at, actor_user_id, actor, action, repo_id, target, detail, ip FROM audit_events
WHERE repo_id = sqlc.arg(repo_id) AND action = sqlc.arg(action) AND id < sqlc.arg(before) AND id >= sqlc.arg(since)
ORDER BY id DESC LIMIT sqlc.arg(limit);

-- name: ListAuditByActor :many
SELECT id, at, actor_user_id, actor, action, repo_id, target, detail, ip FROM audit_events
WHERE actor_user_id = sqlc.arg(actor_user_id) AND id < sqlc.arg(before) AND id >= sqlc.arg(since) ORDER BY id DESC LIMIT sqlc.arg(limit);

-- name: ListAuditByActorAction :many
SELECT id, at, actor_user_id, actor, action, repo_id, target, detail, ip FROM audit_events
WHERE actor_user_id = sqlc.arg(actor_user_id) AND action = sqlc.arg(action) AND id < sqlc.arg(before) AND id >= sqlc.arg(since)
ORDER BY id DESC LIMIT sqlc.arg(limit);

-- name: ListAuditByActorRepo :many
SELECT id, at, actor_user_id, actor, action, repo_id, target, detail, ip FROM audit_events
WHERE actor_user_id = sqlc.arg(actor_user_id) AND repo_id = sqlc.arg(repo_id) AND id < sqlc.arg(before) AND id >= sqlc.arg(since)
ORDER BY id DESC LIMIT sqlc.arg(limit);

-- name: ListAuditByActorRepoAction :many
SELECT id, at, actor_user_id, actor, action, repo_id, target, detail, ip FROM audit_events
WHERE actor_user_id = sqlc.arg(actor_user_id) AND repo_id = sqlc.arg(repo_id) AND action = sqlc.arg(action)
  AND id < sqlc.arg(before) AND id >= sqlc.arg(since) ORDER BY id DESC LIMIT sqlc.arg(limit);

-- The first event at or after a time, through audit_events_at: the id bound of a time filter.
-- name: FirstAuditAt :one
SELECT id FROM audit_events WHERE at >= sqlc.arg(at) ORDER BY at, id LIMIT 1;

-- Retention cleanup in batches, oldest first through audit_events_at.
-- name: DeleteOldAudit :execrows
DELETE FROM audit_events WHERE id IN
  (SELECT x.id FROM audit_events x WHERE x.at < sqlc.arg(before) ORDER BY x.at LIMIT sqlc.arg(limit));

-- A repo delete detaches the events of the repo in batches through audit_events_repo, before
-- the repo row goes (its ON DELETE SET NULL would rewrite them all in one statement).
-- name: ClearAuditRepo :execrows
UPDATE audit_events SET repo_id = NULL WHERE id IN
  (SELECT x.id FROM audit_events x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));
