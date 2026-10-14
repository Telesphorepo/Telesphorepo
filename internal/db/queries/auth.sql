-- Users, API tokens, sessions and repo permissions (internal/auth).

-- name: CreateUser :one
INSERT INTO users (username, password_hash, is_admin, disabled, password_changed_at, created_at)
VALUES (?, ?, ?, 0, ?, ?)
RETURNING id;

-- name: CreateOIDCUser :one
INSERT INTO users (username, password_hash, is_admin, disabled, password_changed_at, created_at, kind, oidc_issuer, oidc_subject, email)
VALUES (?, ?, 1, 0, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: GetUserByUsername :one
SELECT id, username, is_admin, disabled, kind, email, created_at FROM users WHERE username = ?;

-- name: GetUserByID :one
SELECT id, username, is_admin, disabled, kind, email, created_at FROM users WHERE id = ?;

-- name: ListUsers :many
SELECT id, username, is_admin, disabled, kind, email, created_at FROM users
WHERE username > sqlc.arg(after) ORDER BY username LIMIT sqlc.arg(limit);

-- name: HasUsers :one
SELECT EXISTS (SELECT 1 FROM users);

-- name: GetUserLogin :one
SELECT id, password_hash, is_admin, disabled, kind FROM users WHERE username = ?;

-- name: GetUserPasswordHash :one
SELECT password_hash FROM users WHERE id = ?;

-- name: GetPrincipalAndHash :one
SELECT username, is_admin, disabled, password_hash, kind FROM users WHERE id = ?;

-- name: GetPrincipal :one
SELECT username, is_admin, disabled, kind FROM users WHERE id = ?;

-- name: GetUserKind :one
SELECT kind FROM users WHERE id = ?;

-- name: GetUserIDKind :one
SELECT id, kind FROM users WHERE username = ?;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?;

-- name: SetUserAdmin :exec
UPDATE users SET is_admin = ? WHERE id = ?;

-- name: SetUserDisabled :exec
UPDATE users SET disabled = ? WHERE id = ?;

-- name: GetOIDCUser :one
SELECT id, disabled FROM users
WHERE oidc_issuer = ? AND oidc_subject = ? AND oidc_subject IS NOT NULL;

-- name: UpdateOIDCUser :exec
UPDATE users SET is_admin = 1, email = ? WHERE id = ?;

-- name: CreateToken :one
INSERT INTO api_tokens (user_id, public_id, secret_hash, name, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: ListTokens :many
SELECT id, name, created_at, expires_at, last_used_at FROM api_tokens WHERE user_id = ? ORDER BY name;

-- name: DeleteToken :execrows
DELETE FROM api_tokens WHERE id = ?;

-- name: DeleteUserToken :execrows
DELETE FROM api_tokens WHERE id = ? AND user_id = ?;

-- name: GetTokenAuth :one
SELECT t.id AS token_id, t.secret_hash, t.expires_at, t.last_used_at,
       u.id AS user_id, u.username, u.is_admin, u.disabled, u.kind
FROM api_tokens t JOIN users u ON u.id = t.user_id
WHERE t.public_id = ?;

-- name: TouchToken :exec
UPDATE api_tokens SET last_used_at = ? WHERE id = ?;

-- name: CreateSession :exec
INSERT INTO sessions (id_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?);

-- Bounded cleanup, served by sessions_expires.
-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE id_hash IN
  (SELECT x.id_hash FROM sessions x WHERE x.expires_at <= sqlc.arg(now) LIMIT 100);

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id_hash = ?;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = ?;

-- name: DeleteOIDCUserSessions :exec
DELETE FROM sessions WHERE user_id IN
  (SELECT id FROM users WHERE oidc_issuer = ? AND oidc_subject = ? AND oidc_subject IS NOT NULL);

-- name: GetSessionPrincipal :one
SELECT s.expires_at, u.id AS user_id, u.username, u.is_admin, u.disabled, u.kind
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.id_hash = ?;

-- name: GetPermissionLevel :one
SELECT level FROM repo_permissions WHERE repo_id = ? AND user_id = ?;

-- name: ListPermittedRepoIDs :many
SELECT repo_id FROM repo_permissions WHERE user_id = ?;

-- Served by the partial index repos_anonymous.
-- name: ListAnonymousRepoIDs :many
SELECT id FROM repos WHERE anonymous_read = 1;

-- name: SetPermission :execrows
INSERT INTO repo_permissions (repo_id, user_id, level)
SELECT sqlc.arg(repo_id), id, sqlc.arg(level) FROM users WHERE username = sqlc.arg(username)
ON CONFLICT (repo_id, user_id) DO UPDATE SET level = excluded.level;

-- name: DeletePermission :execrows
DELETE FROM repo_permissions
WHERE repo_id = sqlc.arg(repo_id) AND user_id = (SELECT id FROM users WHERE username = sqlc.arg(username));

-- name: ListPermissions :many
SELECT u.username, p.level FROM repo_permissions p JOIN users u ON u.id = p.user_id
WHERE p.repo_id = ? ORDER BY u.username;
