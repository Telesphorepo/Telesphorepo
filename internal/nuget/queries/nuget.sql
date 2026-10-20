-- The nuget_versions table (internal/nuget). Every read of one package goes through its UNIQUE
-- index (repo_id, lower_id, sort_key).

-- name: InsertVersion :exec
INSERT INTO nuget_versions (repo_id, lower_id, package_id, version, normalized, sort_key, semver2, prerelease,
  listed, meta, nupkg_path, asset_id, published_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)
ON CONFLICT (repo_id, lower_id, sort_key) DO UPDATE SET package_id = excluded.package_id,
  version = excluded.version, normalized = excluded.normalized, semver2 = excluded.semver2,
  prerelease = excluded.prerelease, listed = 1, meta = excluded.meta, nupkg_path = excluded.nupkg_path,
  asset_id = excluded.asset_id, published_at = excluded.published_at;

-- Removes the version of a deleted .nupkg, through nuget_versions_asset.
-- name: DeleteVersionsOfAsset :exec
DELETE FROM nuget_versions WHERE asset_id = ?;

-- The versions of one package, oldest first, with the time and the publish session of each file.
-- name: ListVersions :many
SELECT v.package_id, v.version, v.normalized, v.sort_key, v.semver2, v.prerelease, v.listed, v.meta,
  v.nupkg_path, v.published_at, a.size, a.sha256, a.updated_at, a.build_id
FROM nuget_versions v JOIN assets a ON a.id = v.asset_id
WHERE v.repo_id = ? AND v.lower_id = ?
ORDER BY v.sort_key
LIMIT ?;

-- The versions of one package, newest first: the first page of the version browser.
-- name: ListVersionsDesc :many
SELECT v.package_id, v.version, v.normalized, v.sort_key, v.semver2, v.prerelease, v.listed, v.meta,
  v.nupkg_path, v.published_at, a.size, a.sha256, a.updated_at, a.build_id
FROM nuget_versions v JOIN assets a ON a.id = v.asset_id
WHERE v.repo_id = ? AND v.lower_id = ?
ORDER BY v.sort_key DESC
LIMIT ?;

-- The versions of one package below a sort key, newest first: the next page of the version
-- browser.
-- name: ListVersionsBefore :many
SELECT v.package_id, v.version, v.normalized, v.sort_key, v.semver2, v.prerelease, v.listed, v.meta,
  v.nupkg_path, v.published_at, a.size, a.sha256, a.updated_at, a.build_id
FROM nuget_versions v JOIN assets a ON a.id = v.asset_id
WHERE v.repo_id = ? AND v.lower_id = ? AND v.sort_key < ?
ORDER BY v.sort_key DESC
LIMIT ?;

-- One version of a package.
-- name: GetVersion :one
SELECT v.package_id, v.version, v.normalized, v.sort_key, v.semver2, v.prerelease, v.listed, v.meta,
  v.nupkg_path, v.published_at, a.size, a.sha256, a.updated_at, a.build_id
FROM nuget_versions v JOIN assets a ON a.id = v.asset_id
WHERE v.repo_id = ? AND v.lower_id = ? AND v.sort_key = ?;

-- Whether a repo has a package: the group name rule.
-- name: HasPackage :one
SELECT 1 FROM nuget_versions WHERE repo_id = ? AND lower_id = ? LIMIT 1;

-- Unlist (0) or relist (1) one version.
-- name: SetListed :execrows
UPDATE nuget_versions SET listed = ? WHERE repo_id = ? AND lower_id = ? AND sort_key = ?;

-- The package ids of a repo from a lower bound (inclusive) below an upper bound, in id order:
-- the search of a hosted repo pages them.
-- name: ListPackageIDs :many
SELECT DISTINCT lower_id FROM nuget_versions
WHERE repo_id = ? AND lower_id >= ? AND lower_id < ?
ORDER BY lower_id
LIMIT ?;
