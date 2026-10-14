-- Repository records (internal/repos).

-- Indexed point lookup, used on every request.
-- name: GetRepoByName :one
SELECT id, name, format, anonymous_read, config, created_at FROM repos WHERE name = ?;

-- name: GetRepoByID :one
SELECT id, name, format, anonymous_read, config, created_at FROM repos WHERE id = ?;

-- Keyset page by name (pass "" for the first page).
-- name: ListRepos :many
SELECT id, name, format, anonymous_read, config, created_at FROM repos
WHERE name > sqlc.arg(after) ORDER BY name LIMIT sqlc.arg(limit);

-- name: CreateRepo :one
INSERT INTO repos (name, format, anonymous_read, config, created_at) VALUES (?, ?, ?, ?, ?)
RETURNING id;

-- name: UpdateRepo :exec
UPDATE repos SET anonymous_read = ?, config = ? WHERE id = ?;
