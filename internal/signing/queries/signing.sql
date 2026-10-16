-- Repository signing keys (internal/signing). Request paths read the in-memory snapshot; these
-- queries run at startup and on an admin change.

-- Keyset page by name (pass "" for the first page).
-- name: ListSigningKeys :many
SELECT id, name, fingerprint, public_key, private_key, created_at
FROM signing_keys WHERE name > sqlc.arg(after) ORDER BY name LIMIT sqlc.arg(limit);

-- The private key is sealed with the row id, so it is set after the insert.
-- name: CreateSigningKey :one
INSERT INTO signing_keys (name, fingerprint, public_key, private_key, created_at) VALUES (?, ?, ?, X'', ?)
RETURNING id;

-- name: SetSigningKeyPrivate :exec
UPDATE signing_keys SET private_key = ? WHERE id = ?;

-- name: DeleteSigningKey :exec
DELETE FROM signing_keys WHERE id = ?;

-- A keyset page of every binding in repo id order (startup load of the in-memory copy).
-- name: ListRepoSigningKeys :many
SELECT repo_id, key_id FROM repo_signing_keys WHERE repo_id > sqlc.arg(after) ORDER BY repo_id LIMIT sqlc.arg(limit);

-- name: SetRepoSigningKey :exec
INSERT INTO repo_signing_keys (repo_id, key_id) VALUES (?, ?)
ON CONFLICT (repo_id) DO UPDATE SET key_id = excluded.key_id;

-- The repos bound to a key, in repo id order (keyset), through repo_signing_keys_key.
-- name: ListReposOfSigningKey :many
SELECT k.repo_id AS id, r.name FROM repo_signing_keys k JOIN repos r ON r.id = k.repo_id
WHERE k.key_id = sqlc.arg(key_id) AND k.repo_id > sqlc.arg(after)
ORDER BY k.repo_id LIMIT sqlc.arg(limit);