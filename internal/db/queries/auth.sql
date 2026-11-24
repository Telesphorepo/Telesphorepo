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

-- The accounts of usernames (a list of a bulk change, at most a page), through the username index.
-- name: ListUsersNamed :many
SELECT id, username, disabled, kind, email, created_at,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = users.id), '') AS TEXT) AS perms
FROM users WHERE username IN (sqlc.slice(names));

-- The usernames of ids (the uploaders of a page of files), through the primary key.
-- name: ListUsernames :many
SELECT id, username FROM users WHERE id IN (sqlc.slice(ids));

-- name: GetUserByID :one
SELECT id, username, disabled, kind, email, created_at,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = users.id), '') AS TEXT) AS perms
FROM users WHERE id = ?;

-- A keyset page of the accounts whose username is in [lower, upper): the names after the cursor,
-- one range of the username index. A search of the names reads ListUserGrams (names.sql).
-- name: ListUsers :many
SELECT id, username, disabled, kind, email, created_at,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = users.id), '') AS TEXT) AS perms
FROM users
WHERE username >= sqlc.arg(lower) AND username < sqlc.arg(upper) ORDER BY username LIMIT sqlc.arg(limit);

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

-- name: SetUsersDisabled :exec
UPDATE users SET disabled = sqlc.arg(disabled) WHERE id IN (sqlc.slice(ids));

-- name: GetOIDCUser :one
SELECT id, disabled FROM users
WHERE oidc_issuer = ? AND oidc_subject = ? AND oidc_subject IS NOT NULL;

-- name: UpdateOIDCUser :exec
UPDATE users SET email = ? WHERE id = ?;

-- Server permissions.

-- name: ListUserPerms :many
SELECT perm FROM user_permissions WHERE user_id = ?;

-- The server permissions of the users of ids, through the user_permissions primary key.
-- name: ListUsersPerms :many
SELECT user_id, perm FROM user_permissions WHERE user_id IN (sqlc.slice(ids));

-- name: AddUserPerm :exec
INSERT INTO user_permissions (user_id, perm) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: DeleteUserPerms :exec
DELETE FROM user_permissions WHERE user_id = ?;

-- The enabled people who hold perm, through user_permissions_perm.
-- name: ListPermHolders :many
SELECT u.id, u.username, u.kind FROM user_permissions p JOIN users u ON u.id = p.user_id
WHERE p.perm = ? AND u.disabled = 0;

-- Whether user id holds one of perms on some repo, through repo_permissions_user (a seek per
-- permission).
-- name: HasRepoPermIn :one
SELECT EXISTS (SELECT 1 FROM repo_permissions WHERE user_id = sqlc.arg(user_id) AND perm IN (sqlc.slice(perms)));

-- The users of users (a JSON array of ids) who hold one of perms (a JSON array) on some repo,
-- through repo_permissions_user: a seek per user and permission.
-- name: ListUsersWithRepoPermIn :many
SELECT CAST(u.value AS INTEGER) AS user_id FROM json_each(CAST(sqlc.arg(users) AS TEXT)) u
WHERE EXISTS (SELECT 1 FROM repo_permissions p WHERE p.user_id = u.value
  AND p.perm IN (SELECT value FROM json_each(CAST(sqlc.arg(perms) AS TEXT))));

-- name: CreateToken :one
INSERT INTO api_tokens (user_id, public_id, secret_hash, name, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: ListTokens :many
SELECT id, name, created_at, expires_at, last_used_at FROM api_tokens WHERE user_id = ? ORDER BY name;

-- name: DeleteToken :one
DELETE FROM api_tokens WHERE id = ? RETURNING user_id, public_id;

-- name: DeleteUserToken :one
DELETE FROM api_tokens WHERE id = ? AND user_id = ? RETURNING user_id, public_id;

-- name: GetTokenAuth :one
SELECT t.id AS token_id, t.secret_hash, t.expires_at, t.last_used_at,
       u.id AS user_id, u.username, u.disabled, u.kind,
       CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = u.id), '') AS TEXT) AS perms
FROM api_tokens t JOIN users u ON u.id = t.user_id
WHERE t.public_id = ?;

-- The read token behind a core bearer token, by its primary key.
-- name: GetTokenAuthByID :one
SELECT t.id AS token_id, t.expires_at,
       u.id AS user_id, u.username, u.disabled, u.kind,
       CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = u.id), '') AS TEXT) AS perms
FROM api_tokens t JOIN users u ON u.id = t.user_id
WHERE t.id = ?;

-- name: TouchToken :exec
UPDATE api_tokens SET last_used_at = ? WHERE id = ?;

-- The last use of a read token that the edge Worker served, by its public ID; an earlier time
-- than the stored one changes nothing.
-- name: TouchTokenByPublicID :exec
UPDATE api_tokens SET last_used_at = sqlc.arg(last_used_at)
WHERE public_id = sqlc.arg(public_id) AND (last_used_at IS NULL OR last_used_at < sqlc.arg(last_used_at));

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

-- The sessions of the users of ids, served by sessions_user.
-- name: DeleteUsersSessions :exec
DELETE FROM sessions WHERE user_id IN (sqlc.slice(ids));

-- The sessions of a user but one (a password change keeps the session that made it); served by sessions_user.
-- name: DeleteOtherUserSessions :exec
DELETE FROM sessions WHERE user_id = sqlc.arg(user_id) AND id_hash <> sqlc.arg(keep);

-- Moves the end of an open session later, never earlier; one primary-key write.
-- name: ExtendSession :execrows
UPDATE sessions SET expires_at = sqlc.arg(expires_at)
WHERE id_hash = sqlc.arg(id_hash) AND expires_at < sqlc.arg(expires_at);

-- name: GetSessionPrincipal :one
SELECT s.created_at, s.expires_at, u.id AS user_id, u.username, u.disabled, u.kind,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = u.id), '') AS TEXT) AS perms
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.id_hash = ?;

-- Repository permissions.

-- name: ListRepoUserPerms :many
SELECT perm FROM repo_permissions WHERE repo_id = ? AND user_id = ?;

-- Served by the partial index repos_anonymous.
-- name: ListAnonymousRepoIDs :many
SELECT id FROM repos WHERE anonymous_read = 1;

-- A list change of grants (PUT /api/grants) reads and writes every pair of it in one statement
-- each: pairs is a JSON array of [repo_id, user_id], a seek of the primary key per pair (CROSS
-- JOIN keeps the grants the inner loop).
-- name: ListPairPerms :many
SELECT p.repo_id, p.user_id, p.perm
FROM json_each(CAST(sqlc.arg(pairs) AS TEXT)) j CROSS JOIN repo_permissions p
WHERE p.repo_id = json_extract(j.value, '$[0]') AND p.user_id = json_extract(j.value, '$[1]');

-- name: DeletePairPerms :exec
DELETE FROM repo_permissions WHERE (repo_id, user_id) IN
  (SELECT json_extract(j.value, '$[0]'), json_extract(j.value, '$[1]') FROM json_each(CAST(sqlc.arg(pairs) AS TEXT)) j);

-- rows is a JSON array of [repo_id, user_id, perm].
-- name: AddPairPerms :exec
INSERT INTO repo_permissions (repo_id, user_id, perm)
SELECT json_extract(j.value, '$[0]'), json_extract(j.value, '$[1]'), json_extract(j.value, '$[2]')
FROM json_each(CAST(sqlc.arg(rows) AS TEXT)) j WHERE true
ON CONFLICT DO NOTHING;

-- The grants of the users of ids, one row per permission, through repo_permissions_user.
-- name: ListUsersGrants :many
SELECT user_id, repo_id, perm FROM repo_permissions WHERE user_id IN (sqlc.slice(ids));

-- name: ListPermissions :many
SELECT u.username, p.perm FROM repo_permissions p JOIN users u ON u.id = p.user_id
WHERE p.repo_id = ?;

-- Counts the accounts up to a cap, for the Settings overview ("10,000+").
-- name: CountUsersUpTo :one
SELECT count(*) FROM (SELECT 1 FROM users LIMIT sqlc.arg(cap));

