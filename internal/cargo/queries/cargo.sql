-- The cargo_* tables (internal/cargo).

-- The name that holds a folded crate name, through the cargo_names primary key.
-- name: GetName :one
SELECT name FROM cargo_names WHERE repo_id = ? AND folded_name = ?;

-- Records the name of a published version and moves the revision of the crate on.
-- name: InsertName :exec
INSERT INTO cargo_names (repo_id, folded_name, name, rev) VALUES (?, ?, ?, ?)
ON CONFLICT (repo_id, folded_name) DO UPDATE SET rev = max(cargo_names.rev + 1, excluded.rev);

-- Moves the revision of a crate on after a removal or a yank: rev becomes max(rev + 1, at).
-- name: BumpRev :exec
UPDATE cargo_names SET rev = max(rev + 1, CAST(sqlc.arg(at) AS INTEGER))
WHERE repo_id = sqlc.arg(repo_id) AND folded_name = sqlc.arg(folded_name);

-- The name and the revision of a crate, through the cargo_names primary key.
-- name: GetNameRev :one
SELECT name, rev FROM cargo_names WHERE repo_id = ? AND folded_name = ?;

-- name: DeleteName :exec
DELETE FROM cargo_names WHERE repo_id = ? AND folded_name = ?;

-- The version of a crate that equals a version ignoring build metadata, through the UNIQUE index.
-- name: GetVersionByBase :one
SELECT version, path FROM cargo_versions WHERE repo_id = ? AND lower_name = ? AND base_key = ?;

-- name: InsertVersion :exec
INSERT INTO cargo_versions (repo_id, name, lower_name, version, sort_key, base_key, entry, asset_id, path, yanked, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- The index entries of a crate in version order, through cargo_versions_sort.
-- name: ListEntries :many
SELECT entry, yanked FROM cargo_versions
WHERE repo_id = ? AND lower_name = ?
ORDER BY sort_key
LIMIT ?;

-- Whether a repo has a crate: its name as published, through cargo_versions_sort.
-- name: CrateName :one
SELECT name FROM cargo_versions WHERE repo_id = ? AND lower_name = ? LIMIT 1;

-- name: SetYanked :execrows
UPDATE cargo_versions SET yanked = ?
WHERE repo_id = ? AND lower_name = ? AND sort_key = ?;

-- Removes the version of a deleted .crate file through cargo_versions_asset.
-- name: DeleteVersionsOfAsset :many
DELETE FROM cargo_versions WHERE asset_id = ?
RETURNING repo_id, name, lower_name;

-- A page of the versions of a crate, newest first, below the sort key of the cursor, with the
-- file of each, through cargo_versions_sort.
-- name: ListVersionsPage :many
SELECT v.name, v.version, v.path, v.yanked, a.size, a.sha256, a.updated_at, a.build_id
FROM cargo_versions v JOIN assets a ON a.id = v.asset_id
WHERE v.repo_id = ? AND v.lower_name = ? AND v.sort_key < ?
ORDER BY v.sort_key DESC
LIMIT ?;

-- One version of a crate with its file, through cargo_versions_sort.
-- name: GetVersion :one
SELECT v.name, v.version, v.path, v.yanked, a.size, a.sha256, a.updated_at, a.build_id
FROM cargo_versions v JOIN assets a ON a.id = v.asset_id
WHERE v.repo_id = ? AND v.lower_name = ? AND v.sort_key = ?;

-- A synthetic version of the scale test ("telesphorepo-server seed"); a repeated seed keeps it.
-- name: SeedVersion :exec
INSERT INTO cargo_versions (repo_id, name, lower_name, version, sort_key, base_key, entry, asset_id, path, yanked, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
ON CONFLICT (repo_id, lower_name, base_key) DO NOTHING;
