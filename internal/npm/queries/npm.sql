-- The npm_* tables (internal/npm).

-- name: GetPackage :one
SELECT created, modified, rev FROM npm_packages WHERE repo_id = ? AND name = ?;

-- name: UpsertPackage :exec
INSERT INTO npm_packages (repo_id, name, created, modified, rev) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (repo_id, name) DO UPDATE SET modified = excluded.modified, rev = excluded.rev;

-- name: DeletePackage :exec
DELETE FROM npm_packages WHERE npm_packages.repo_id = ? AND name = ?;

-- name: InsertVersion :exec
INSERT INTO npm_versions (repo_id, name, version, sort_key, manifest, tarball, integrity, shasum, published_at, deprecated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- The tarball of a version: the duplicate check of a publish.
-- name: GetVersionTarball :one
SELECT tarball FROM npm_versions WHERE repo_id = ? AND name = ? AND version = ?;

-- One version with its manifest, through the primary key: the manifest of GET <name>/<version>.
-- name: GetVersion :one
SELECT tarball, integrity, shasum, deprecated, manifest FROM npm_versions WHERE repo_id = ? AND name = ? AND version = ?;

-- Every version of a package in SemVer order, through npm_versions_sort.
-- name: ListVersions :many
SELECT version, sort_key, manifest, tarball, integrity, shasum, published_at, deprecated
FROM npm_versions
WHERE repo_id = ? AND name = ?
ORDER BY sort_key
LIMIT ?;

-- Every version of a package in SemVer order without its manifest: what the package row and the
-- latest tag follow after a change.
-- name: ListVersionStates :many
SELECT version, published_at, deprecated
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
SELECT v.version, v.tarball, v.published_at, a.size, a.sha256, a.build_id, a.updated_at
FROM npm_versions v JOIN assets a ON a.repo_id = v.repo_id AND a.path = v.tarball
WHERE v.repo_id = ? AND v.name = ? AND v.version = ?;

-- Removes the versions of the deleted tarballs of a remove batch (a JSON array of paths), through
-- npm_versions_tarball (the row value keeps the planner off a range of the primary key).
-- name: DeleteVersionsOfTarballs :many
DELETE FROM npm_versions WHERE (repo_id, tarball) IN (SELECT CAST(sqlc.arg(repo_id) AS INTEGER), value FROM json_each(sqlc.arg(tarballs)))
RETURNING name, version, tarball;

-- name: SetDeprecated :exec
UPDATE npm_versions SET deprecated = ? WHERE repo_id = ? AND name = ? AND version = ?;

-- name: ListTags :many
SELECT tag, version FROM npm_tags WHERE repo_id = ? AND name = ? ORDER BY tag;

-- The version of one dist-tag, through the primary key.
-- name: GetTag :one
SELECT version FROM npm_tags WHERE repo_id = ? AND name = ? AND tag = ?;

-- A keyset page of every dist-tag of a repo by package and tag, through the primary key, each with
-- whether the repo into has the package and the version of into's tag of the same name ('' for
-- none), through the primary keys (a merge).
-- name: ListRepoTagsAfter :many
SELECT f.name, f.tag, f.version,
  EXISTS (SELECT 1 FROM npm_packages p WHERE p.repo_id = sqlc.arg(into_id) AND p.name = f.name) AS owned,
  CAST(coalesce((SELECT t.version FROM npm_tags t WHERE t.repo_id = sqlc.arg(into_id) AND t.name = f.name AND t.tag = f.tag), '') AS TEXT) AS target
FROM npm_tags f
WHERE f.repo_id = sqlc.arg(repo_id) AND (f.name, f.tag) > (sqlc.arg(name), sqlc.arg(tag))
ORDER BY f.name, f.tag LIMIT sqlc.arg(limit);

-- name: UpsertTag :exec
INSERT INTO npm_tags (repo_id, name, tag, version) VALUES (?, ?, ?, ?)
ON CONFLICT (repo_id, name, tag) DO UPDATE SET version = excluded.version;

-- name: DeleteTag :exec
DELETE FROM npm_tags WHERE npm_tags.repo_id = ? AND name = ? AND tag = ?;

-- name: DeleteTagsOfPackage :exec
DELETE FROM npm_tags WHERE npm_tags.repo_id = ? AND name = ?;

-- A repo delete empties these tables batch by batch before its row goes (repos.Service.OnPurge).
-- name: PurgeNpmTags :execrows
DELETE FROM npm_tags WHERE npm_tags.repo_id = sqlc.arg(repo_id) AND (name, tag) IN (SELECT x.name, x.tag FROM npm_tags x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- name: PurgeNpmVersions :execrows
DELETE FROM npm_versions WHERE npm_versions.repo_id = sqlc.arg(repo_id) AND (name, version) IN (SELECT x.name, x.version FROM npm_versions x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- name: PurgeNpmPackages :execrows
DELETE FROM npm_packages WHERE npm_packages.repo_id = sqlc.arg(repo_id) AND name IN (SELECT x.name FROM npm_packages x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

