-- The deb_* tables (internal/debian). Packages rows are streamed by the hand-written
-- StreamPackages in dbq/extra.go.

-- Upload duplicate check: point lookup on the deb_packages UNIQUE index.
-- name: GetPackageSHA256 :one
SELECT sha256 FROM deb_packages
WHERE repo_id = ? AND distribution = ? AND component = ? AND architecture = ? AND package = ? AND version = ?;

-- Import duplicate check: the pool file of a package with the same bytes in the (dist,
-- component), found through deb_packages_sha256 before the file is downloaded.
-- name: GetPackageFileBySHA256 :one
SELECT filename FROM deb_packages
WHERE repo_id = ? AND distribution = ? AND component = ? AND sha256 = ? LIMIT 1;

-- The .deb files of one package in one component, with the time and the publish session of
-- each file, through deb_packages_package. A file in several distributions has one row for each.
-- name: ListPackageFiles :many
SELECT p.distribution, p.architecture, p.version, p.filename, p.size, p.sha256,
  a.updated_at, a.publish_session_id
FROM deb_packages p JOIN assets a ON a.id = p.asset_id
WHERE p.repo_id = ? AND p.component = ? AND p.package = ?
LIMIT ?;

-- The package of a pool file, through deb_packages_asset.
-- name: GetPackageOfAsset :one
SELECT distribution, component, package, version FROM deb_packages
WHERE asset_id = ?
LIMIT 1;

-- name: InsertPackage :exec
INSERT INTO deb_packages (repo_id, distribution, component, architecture, package, version, asset_id,
  filename, size, md5, sha1, sha256, control, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- The (distribution, component) pairs of a repo that hold packages, in UNIQUE index order.
-- name: ListPackageComponents :many
SELECT DISTINCT distribution, component FROM deb_packages
WHERE repo_id = ?
ORDER BY distribution, component;

-- name: MarkDirty :exec
INSERT INTO deb_dirty (repo_id, distribution, component, architecture, marked_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (repo_id, distribution, component, architecture) DO UPDATE SET marked_at = excluded.marked_at;

-- name: CountDirty :one
SELECT COUNT(*) FROM deb_dirty WHERE repo_id = ?;

-- name: ListDirtyUnits :many
SELECT component, architecture FROM deb_dirty
WHERE repo_id = ? AND distribution = ?
ORDER BY component, architecture;

-- name: ListRepoDirtyDists :many
SELECT DISTINCT distribution FROM deb_dirty
WHERE repo_id = ?
ORDER BY distribution;

-- Deletes a dirty unit unless it was marked again after the rebuild started.
-- name: ClearDirty :exec
DELETE FROM deb_dirty
WHERE repo_id = ? AND distribution = ? AND component = ? AND architecture = ? AND marked_at <= ?;

-- name: HasDirty :one
SELECT EXISTS (SELECT 1 FROM deb_dirty WHERE repo_id = ? AND distribution = ?);

-- name: DeleteDirtyArch :exec
DELETE FROM deb_dirty WHERE repo_id = ? AND architecture = ?;

-- Startup recovery: every (repo, dist) with a pending rebuild.
-- name: ListAllDirtyDists :many
SELECT DISTINCT repo_id, distribution FROM deb_dirty
ORDER BY repo_id, distribution;

-- Startup recovery: the newest mark, so the rebuild clock never goes back.
-- name: MaxDirtyMark :one
SELECT CAST(COALESCE(MAX(marked_at), 0) AS INTEGER) AS max_mark FROM deb_dirty;

-- name: UpsertIndexFile :exec
INSERT INTO deb_index_files (repo_id, distribution, path, size, md5, sha256, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (repo_id, distribution, path) DO UPDATE SET size = excluded.size, md5 = excluded.md5,
  sha256 = excluded.sha256, updated_at = excluded.updated_at;

-- The Release inputs of one dist, in path order.
-- name: ListIndexFiles :many
SELECT path, size, md5, sha256 FROM deb_index_files
WHERE repo_id = ? AND distribution = ?
ORDER BY path;

-- name: ListRepoIndexFiles :many
SELECT distribution, path FROM deb_index_files
WHERE repo_id = ?
ORDER BY distribution, path;

-- name: DeleteIndexFile :exec
DELETE FROM deb_index_files WHERE repo_id = ? AND distribution = ? AND path = ?;

-- The current hash of one index file: the generation a rebuild replaces.
-- name: GetIndexFileSHA256 :one
SELECT sha256 FROM deb_index_files WHERE repo_id = ? AND distribution = ? AND path = ?;

-- Removes the packages of a deleted pool file (one row per dist and component that holds it)
-- through deb_packages_asset.
-- name: DeletePackagesOfAsset :many
DELETE FROM deb_packages WHERE asset_id = ?
RETURNING distribution, component, architecture;

-- Removes the asset row of an index file, so it is no longer served.
-- name: DeleteIndexAsset :exec
DELETE FROM assets WHERE repo_id = ? AND path = ? AND kind = 'index';
