-- The npm_* tables (internal/npm).

-- name: GetPackage :one
SELECT created, modified, rev FROM npm_packages WHERE repo_id = ? AND name = ?;

-- name: UpsertPackage :exec
INSERT INTO npm_packages (repo_id, name, created, modified, rev) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (repo_id, name) DO UPDATE SET modified = excluded.modified, rev = excluded.rev;

-- name: DeletePackage :exec
DELETE FROM npm_packages WHERE repo_id = ? AND name = ?;

-- name: InsertVersion :exec
INSERT INTO npm_versions (repo_id, name, version, sort_key, manifest, tarball, integrity, shasum, published_at, deprecated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- The tarball of a version: the duplicate check of a publish.
-- name: GetVersionTarball :one
SELECT tarball FROM npm_versions WHERE repo_id = ? AND name = ? AND version = ?;

-- Every version of a package in SemVer order, through npm_versions_sort.
-- name: ListVersions :many
SELECT version, sort_key, manifest, tarball, integrity, shasum, published_at, deprecated
FROM npm_versions
WHERE repo_id = ? AND name = ?
ORDER BY sort_key
LIMIT ?;

-- A page of the versions of a package, newest first, below a sort key, with the CI build record of
-- each tarball.
-- name: ListVersionsBefore :many
SELECT v.version, v.tarball, v.published_at, a.size, a.sha256, a.build_id
FROM npm_versions v JOIN assets a ON a.repo_id = v.repo_id AND a.path = v.tarball
WHERE v.repo_id = ? AND v.name = ? AND v.sort_key < ?
ORDER BY v.sort_key DESC
LIMIT ?;

-- One version with its tarball asset.
-- name: GetVersionFile :one
SELECT v.version, v.tarball, v.published_at, a.size, a.sha256, a.build_id
FROM npm_versions v JOIN assets a ON a.repo_id = v.repo_id AND a.path = v.tarball
WHERE v.repo_id = ? AND v.name = ? AND v.version = ?;

-- The version of a tarball, through npm_versions_tarball.
-- name: GetVersionOfTarball :one
SELECT name, version FROM npm_versions WHERE repo_id = ? AND tarball = ?;

-- name: DeleteVersion :exec
DELETE FROM npm_versions WHERE repo_id = ? AND name = ? AND version = ?;

-- name: SetDeprecated :exec
UPDATE npm_versions SET deprecated = ? WHERE repo_id = ? AND name = ? AND version = ?;

-- name: ListTags :many
SELECT tag, version FROM npm_tags WHERE repo_id = ? AND name = ? ORDER BY tag;

-- name: UpsertTag :exec
INSERT INTO npm_tags (repo_id, name, tag, version) VALUES (?, ?, ?, ?)
ON CONFLICT (repo_id, name, tag) DO UPDATE SET version = excluded.version;

-- name: DeleteTag :exec
DELETE FROM npm_tags WHERE repo_id = ? AND name = ? AND tag = ?;

-- name: DeleteTagsOfPackage :exec
DELETE FROM npm_tags WHERE repo_id = ? AND name = ?;
