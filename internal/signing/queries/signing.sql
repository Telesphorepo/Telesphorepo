-- Repository signing keys (internal/signing). Request paths read the in-memory snapshot; these
-- queries run at startup and on an admin change, but for the search of the keys.

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

-- The search of the keys (internal/namesearch, migration 0002_key_search.sql): the names of the
-- keys in the rows of one gram whose name holds the text.
-- name: SearchSigningKeyNames :many
SELECT k.name FROM signing_key_grams g JOIN signing_keys k ON k.id = g.key_id
WHERE g.gram = sqlc.arg(gram) AND instr(g.name, sqlc.arg(text)) > 0;

-- The names of the keys in the rows of one trigram whose fingerprint holds a key ID.
-- name: SearchSigningKeyFingerprints :many
SELECT k.name FROM signing_key_grams g JOIN signing_keys k ON k.id = g.key_id
WHERE g.gram = sqlc.arg(gram) AND instr(g.fingerprint, sqlc.arg(text)) > 0;

-- The probe of a gram: its rows, up to limit.
-- name: CountSigningKeyGram :one
SELECT count(*) FROM (SELECT 1 FROM signing_key_grams WHERE gram = sqlc.arg(gram) LIMIT sqlc.arg(limit));

-- name: InsertSigningKeyGram :exec
INSERT OR IGNORE INTO signing_key_grams (gram, key_id, name, fingerprint) VALUES (?, ?, ?, ?);

-- name: DeleteSigningKey :exec
DELETE FROM signing_keys WHERE id = ?;

-- A keyset page of every binding in repo id order (startup load of the in-memory copy).
-- name: ListRepoSigningKeys :many
SELECT repo_id, key_id FROM repo_signing_keys WHERE repo_id > sqlc.arg(after) ORDER BY repo_id LIMIT sqlc.arg(limit);

-- name: SetRepoSigningKey :exec
INSERT INTO repo_signing_keys (repo_id, key_id) VALUES (?, ?)
ON CONFLICT (repo_id) DO UPDATE SET key_id = excluded.key_id;

-- The repos bound to a set of keys (a JSON array of ids), through repo_signing_keys_key: one query
-- for the keys of a list, which the caller orders by repo id.
-- name: ListReposOfSigningKeys :many
SELECT k.key_id, k.repo_id, r.name FROM repo_signing_keys k JOIN repos r ON r.id = k.repo_id
WHERE k.key_id IN (SELECT value FROM json_each(sqlc.arg(key_ids)));

-- The repos bound to a key, in repo id order (keyset), through repo_signing_keys_key.
-- name: ListReposOfSigningKey :many
SELECT k.repo_id AS id, r.name FROM repo_signing_keys k JOIN repos r ON r.id = k.repo_id
WHERE k.key_id = sqlc.arg(key_id) AND k.repo_id > sqlc.arg(after)
ORDER BY k.repo_id LIMIT sqlc.arg(limit);