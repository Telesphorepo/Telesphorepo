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


-- The package of a pool file, through deb_packages_asset.
-- name: GetPackageOfAsset :one
SELECT distribution, component, package, version FROM deb_packages
WHERE asset_id = ?
LIMIT 1;

-- name: InsertPackage :one
INSERT INTO deb_packages (repo_id, distribution, component, architecture, package, version, asset_id,
  filename, size, md5, sha1, sha256, control, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- The order keys of a deb_packages row (order.go).
-- name: InsertPackageOrder :exec
INSERT INTO deb_package_order (package_id, repo_id, folder, component, package, name_key, build_key)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- Whether a package folder has a file, through deb_package_order_page.
-- name: PackageHasFiles :one
SELECT EXISTS (SELECT 1 FROM deb_package_order WHERE repo_id = ? AND folder = ? AND component = ? AND package = ?);

-- The (distribution, component) pairs of a repo that hold packages, in UNIQUE index order.
-- name: ListPackageComponents :many
SELECT DISTINCT distribution, component FROM deb_packages
WHERE repo_id = ?
ORDER BY distribution, component;

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
DELETE FROM deb_index_files WHERE deb_index_files.repo_id = ? AND distribution = ? AND path = ?;

-- The current hash of one index file: the generation a rebuild replaces.
-- name: GetIndexFileSHA256 :one
SELECT sha256 FROM deb_index_files WHERE repo_id = ? AND distribution = ? AND path = ?;

-- Removes the packages of a deleted pool file (one row per dist and component that holds it)
-- through deb_packages_asset.
-- name: DeletePackagesOfAsset :many
DELETE FROM deb_packages WHERE asset_id = ?
RETURNING distribution, component, architecture;


-- A repo delete empties these tables batch by batch before its row goes (repos.Service.OnPurge).
-- name: PurgeDebPackages :execrows
DELETE FROM deb_packages WHERE id IN (SELECT x.id FROM deb_packages x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- name: PurgeDebIndexFiles :execrows
DELETE FROM deb_index_files WHERE deb_index_files.repo_id = sqlc.arg(repo_id) AND (distribution, path) IN (SELECT x.distribution, x.path FROM deb_index_files x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

