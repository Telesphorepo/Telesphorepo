-- The rpm_* tables (internal/rpm). The repodata streams through the hand-written queries of
-- dbq/extra.go.

-- Upload duplicate check: a point lookup on the UNIQUE index.
-- name: GetSums :one
SELECT upload_sha256, sha256 FROM rpm_packages
WHERE repo_id = ? AND name = ? AND epoch = ? AND version = ? AND release = ? AND arch = ?;

-- name: InsertPackage :one
INSERT INTO rpm_packages (repo_id, name, epoch, version, release, arch, evr, vkey, asset_id, filename, size, sha256,
  upload_sha256, format, hdr_state, header_start, header_len, build_time, installed_size, archive_size, summary, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: InsertParts :exec
INSERT INTO rpm_package_parts (package_id, text, format_a, format_b, files, other) VALUES (?, ?, ?, ?, ?, ?);

-- A re-signed file: the stored file's columns.
-- name: UpdateStored :exec
UPDATE rpm_packages SET asset_id = ?, size = ?, sha256 = ?, header_start = ? WHERE id = ?;

-- The package of a stored file, through rpm_packages_asset.
-- name: GetPackageOfAsset :one
SELECT id, name, epoch, version, release, arch, evr, filename, size, sha256, summary, build_time, installed_size, format
FROM rpm_packages WHERE asset_id = ? LIMIT 1;

-- A package of the identity of a file name (any epoch) with the uploaded bytes sha256: the
-- Nexus import skips it without a download. A seek of the UNIQUE index by name.
-- name: FindUpload :one
SELECT filename FROM rpm_packages
WHERE repo_id = ? AND name = ? AND version = ? AND release = ? AND arch = ? AND upload_sha256 = ? LIMIT 1;

-- name: DeletePackage :exec
DELETE FROM rpm_packages WHERE id = ?;

-- Whether a package name has a file, through rpm_packages_order.
-- name: PackageHasFiles :one
SELECT EXISTS (SELECT 1 FROM rpm_packages WHERE repo_id = ? AND name = ?);

-- The number of packages of a repo, through rpm_packages_order (the count of the repodata).
-- name: CountPackages :one
SELECT count(*) FROM rpm_packages WHERE repo_id = ?;

-- name: GetRepodata :one
SELECT hrefs FROM rpm_repodata WHERE repo_id = ?;

-- name: SetRepodata :exec
INSERT INTO rpm_repodata (repo_id, hrefs) VALUES (?, ?)
ON CONFLICT (repo_id) DO UPDATE SET hrefs = excluded.hrefs;

-- A repo delete empties these tables batch by batch before its row goes (repos.Service.OnPurge).
-- name: PurgePackages :execrows
DELETE FROM rpm_packages WHERE id IN (SELECT x.id FROM rpm_packages x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- The parts of the primary entry of a package (the package details of the web UI).
-- name: GetParts :one
SELECT text, format_a, format_b FROM rpm_package_parts WHERE package_id = ?;