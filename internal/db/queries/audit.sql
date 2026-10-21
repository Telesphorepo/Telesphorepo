-- The audit log (internal/audit).

-- name: InsertAuditEvent :exec
INSERT INTO audit_events (at, actor_user_id, actor, action, repo_id, target, detail, ip)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- Keyset pages newest first: ids below before. Each filter has its own index.
-- name: ListAudit :many
SELECT id, at, actor_user_id, actor, action, repo_id, target, detail, ip FROM audit_events
WHERE id < sqlc.arg(before) ORDER BY id DESC LIMIT sqlc.arg(limit);

-- name: ListAuditByAction :many
SELECT id, at, actor_user_id, actor, action, repo_id, target, detail, ip FROM audit_events
WHERE action = sqlc.arg(action) AND id < sqlc.arg(before) ORDER BY id DESC LIMIT sqlc.arg(limit);

-- name: ListAuditByRepo :many
SELECT id, at, actor_user_id, actor, action, repo_id, target, detail, ip FROM audit_events
WHERE repo_id = sqlc.arg(repo_id) AND id < sqlc.arg(before) ORDER BY id DESC LIMIT sqlc.arg(limit);

-- audit_events_repo_action serves the repo and the action together.
-- name: ListAuditByRepoAction :many
SELECT id, at, actor_user_id, actor, action, repo_id, target, detail, ip FROM audit_events
WHERE repo_id = sqlc.arg(repo_id) AND action = sqlc.arg(action) AND id < sqlc.arg(before)
ORDER BY id DESC LIMIT sqlc.arg(limit);

-- Retention cleanup in batches, oldest first through audit_events_at.
-- name: DeleteOldAudit :execrows
DELETE FROM audit_events WHERE id IN
  (SELECT x.id FROM audit_events x WHERE x.at < sqlc.arg(before) ORDER BY x.at LIMIT sqlc.arg(limit));

-- A repo delete detaches the events of the repo in batches through audit_events_repo, before
-- the repo row goes (its ON DELETE SET NULL would rewrite them all in one statement).
-- name: ClearAuditRepo :execrows
UPDATE audit_events SET repo_id = NULL WHERE id IN
  (SELECT x.id FROM audit_events x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));
