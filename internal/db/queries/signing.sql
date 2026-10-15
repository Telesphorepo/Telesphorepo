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

-- The repos whose config names a key, through repos_signing_key, in id order (keyset).
-- name: ListReposUsingSigningKey :many
SELECT id, name FROM repos
WHERE json_extract(config, '$.signing_key') = CAST(sqlc.arg(key) AS TEXT) AND id > sqlc.arg(after)
ORDER BY id LIMIT sqlc.arg(limit);
