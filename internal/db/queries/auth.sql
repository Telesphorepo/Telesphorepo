-- Users, API tokens, sessions and permissions (internal/auth).
--
-- A perms column is the user's server permissions joined with commas ("" for none), read through
-- the user_permissions primary key.

-- name: CreateUser :one
INSERT INTO users (username, password_hash, disabled, password_changed_at, created_at)
VALUES (?, ?, 0, ?, ?)
RETURNING id;

-- name: CreateOIDCUser :one
INSERT INTO users (username, password_hash, disabled, password_changed_at, created_at, kind, oidc_issuer, oidc_subject, email)
VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: GetUserByUsername :one
SELECT id, username, disabled, kind, email, created_at,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = users.id), '') AS TEXT) AS perms
FROM users WHERE username = ?;

-- name: GetUserByID :one
SELECT id, username, disabled, kind, email, created_at,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = users.id), '') AS TEXT) AS perms
FROM users WHERE id = ?;

-- name: ListUsers :many
SELECT id, username, disabled, kind, email, created_at,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = users.id), '') AS TEXT) AS perms
FROM users
WHERE username > sqlc.arg(after) ORDER BY username LIMIT sqlc.arg(limit);

-- The enabled people whose username is in [lower, upper), for a person picker.
-- name: ListPeople :many
SELECT username FROM users
WHERE username >= sqlc.arg(lower) AND username < sqlc.arg(upper) AND disabled = 0
ORDER BY username LIMIT sqlc.arg(limit);

-- name: HasUsers :one
SELECT EXISTS (SELECT 1 FROM users);

-- name: GetUserLogin :one
SELECT id, password_hash, disabled, kind,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = users.id), '') AS TEXT) AS perms
FROM users WHERE username = ?;

-- name: GetUserPasswordHash :one
SELECT password_hash FROM users WHERE id = ?;

-- name: GetPrincipalAndHash :one
SELECT username, disabled, password_hash, kind,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = users.id), '') AS TEXT) AS perms
FROM users WHERE id = ?;

-- name: GetPrincipal :one
SELECT username, disabled, kind,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = users.id), '') AS TEXT) AS perms
FROM users WHERE id = ?;

-- name: GetUserKind :one
SELECT kind FROM users WHERE id = ?;

-- name: GetUserIDKind :one
SELECT id, kind FROM users WHERE username = ?;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?;

-- name: SetUserDisabled :exec
UPDATE users SET disabled = ? WHERE id = ?;

-- name: GetOIDCUser :one
SELECT id, disabled FROM users
WHERE oidc_issuer = ? AND oidc_subject = ? AND oidc_subject IS NOT NULL;

-- name: UpdateOIDCUser :exec
UPDATE users SET email = ? WHERE id = ?;

-- Server permissions.

-- name: ListUserPerms :many
SELECT perm FROM user_permissions WHERE user_id = ?;

-- name: AddUserPerm :exec
INSERT INTO user_permissions (user_id, perm) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: DeleteUserPerms :exec
DELETE FROM user_permissions WHERE user_id = ?;

-- The enabled people who hold perm, through user_permissions_perm.
-- name: ListPermHolders :many
SELECT u.id, u.username, u.kind FROM user_permissions p JOIN users u ON u.id = p.user_id
WHERE p.perm = ? AND u.disabled = 0;

-- Whether user id needs the second factor: any server permission, or a repository permission
-- other than download and publish.
-- name: UserElevated :one
SELECT EXISTS (SELECT 1 FROM user_permissions up WHERE up.user_id = sqlc.arg(user_id))
    OR EXISTS (SELECT 1 FROM repo_permissions rp WHERE rp.user_id = sqlc.arg(user_id) AND rp.perm NOT IN ('download','publish'));

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
       u.id AS user_id, u.username, u.disabled, u.kind,
       CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = u.id), '') AS TEXT) AS perms
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

-- elevated: the user holds a repository permission other than download and publish.
-- name: GetSessionPrincipal :one
SELECT s.expires_at, u.id AS user_id, u.username, u.disabled, u.kind,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = u.id), '') AS TEXT) AS perms,
  EXISTS (SELECT 1 FROM repo_permissions rp WHERE rp.user_id = u.id AND rp.perm NOT IN ('download','publish')) AS manages_repo
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.id_hash = ?;

-- Repository permissions.

-- name: ListRepoUserPerms :many
SELECT perm FROM repo_permissions WHERE repo_id = ? AND user_id = ?;

-- The repos where user id holds a permission (a repo once per permission), through
-- repo_permissions_user.
-- name: ListPermittedRepoIDs :many
SELECT repo_id FROM repo_permissions WHERE user_id = ?;

-- Served by the partial index repos_anonymous.
-- name: ListAnonymousRepoIDs :many
SELECT id FROM repos WHERE anonymous_read = 1;

-- name: AddRepoPerm :exec
INSERT INTO repo_permissions (repo_id, user_id, perm) VALUES (?, ?, ?) ON CONFLICT DO NOTHING;

-- name: DeleteRepoUserPerms :execrows
DELETE FROM repo_permissions WHERE repo_id = ? AND user_id = ?;

-- name: ListPermissions :many
SELECT u.username, p.perm FROM repo_permissions p JOIN users u ON u.id = p.user_id
WHERE p.repo_id = ?;
