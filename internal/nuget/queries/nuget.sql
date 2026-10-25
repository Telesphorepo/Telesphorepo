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
-- name: DeleteVersionsOfAsset :many
DELETE FROM nuget_versions WHERE asset_id = ?
RETURNING lower_id;

-- Records a package with a changed version and moves its revision on.
-- name: UpsertPackage :exec
INSERT INTO nuget_packages (repo_id, lower_id, rev) VALUES (?, ?, ?)
ON CONFLICT (repo_id, lower_id) DO UPDATE SET rev = max(nuget_packages.rev + 1, excluded.rev);

-- The revision of a package of a hosted repo, through the UNIQUE index: none when the repo has
-- no version of it.
-- name: GetPackageRev :one
SELECT rev FROM nuget_packages WHERE repo_id = ? AND lower_id = ?;

-- name: DeletePackage :exec
DELETE FROM nuget_packages WHERE repo_id = ? AND lower_id = ?;

-- The newest listed release of a package (nuget_package_latest), NULL when it has none: the
-- version of the artifact snippets.
-- name: GetNewestRelease :one
SELECT l.release2 FROM nuget_packages p JOIN nuget_package_latest l ON l.package = p.id
WHERE p.repo_id = ? AND p.lower_id = ?;

-- The versions of one package, oldest first, without their metadata: the flat version list.
-- name: ListVersionNames :many
SELECT normalized FROM nuget_versions
WHERE repo_id = ? AND lower_id = ?
ORDER BY sort_key
LIMIT ?;

-- Writes the newest listed version of each search variant of a package (nuget_package_latest),
-- with each move of its rev. Each one walks the UNIQUE index from the newest version and stops at
-- the first match.
-- name: SetPackageLatest :exec
INSERT INTO nuget_package_latest (package, release, release2, pre, pre2)
SELECT p.id,
  (SELECT v.sort_key FROM nuget_versions v WHERE v.repo_id = p.repo_id AND v.lower_id = p.lower_id
    AND v.listed = 1 AND v.prerelease = 0 AND v.semver2 = 0 ORDER BY v.sort_key DESC LIMIT 1),
  (SELECT v.sort_key FROM nuget_versions v WHERE v.repo_id = p.repo_id AND v.lower_id = p.lower_id
    AND v.listed = 1 AND v.prerelease = 0 ORDER BY v.sort_key DESC LIMIT 1),
  (SELECT v.sort_key FROM nuget_versions v WHERE v.repo_id = p.repo_id AND v.lower_id = p.lower_id
    AND v.listed = 1 AND v.semver2 = 0 ORDER BY v.sort_key DESC LIMIT 1),
  (SELECT v.sort_key FROM nuget_versions v WHERE v.repo_id = p.repo_id AND v.lower_id = p.lower_id
    AND v.listed = 1 ORDER BY v.sort_key DESC LIMIT 1)
FROM nuget_packages p
WHERE p.repo_id = ? AND p.lower_id = ?
ON CONFLICT (package) DO UPDATE SET release = excluded.release, release2 = excluded.release2,
  pre = excluded.pre, pre2 = excluded.pre2;

-- The versions of a package that a search with prereleases shows, oldest first, without their
-- metadata.
-- name: ListVisibleVersions :many
SELECT version, normalized FROM nuget_versions
WHERE repo_id = ? AND lower_id = ? AND listed = 1 AND semver2 <= ?
ORDER BY sort_key
LIMIT ?;

-- The versions of a package that a search without prereleases shows, oldest first, through the
-- partial index of the listed releases.
-- name: ListVisibleReleases :many
SELECT version, normalized FROM nuget_versions
WHERE repo_id = ? AND lower_id = ? AND listed = 1 AND prerelease = 0 AND semver2 <= ?
ORDER BY sort_key
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

-- The versions of one package that a registration variant shows (semver2 1: SemVer 2.0 ones
-- too), oldest first: an index that inlines its leaves.
-- name: ListRegVersions :many
SELECT v.package_id, v.version, v.normalized, v.sort_key, v.semver2, v.prerelease, v.listed, v.meta,
  v.nupkg_path, v.published_at, a.size, a.sha256, a.updated_at, a.build_id
FROM nuget_versions v JOIN assets a ON a.id = v.asset_id
WHERE v.repo_id = ? AND v.lower_id = ? AND v.semver2 <= ?
ORDER BY v.sort_key
LIMIT ?;

-- The versions of one package that a registration variant shows, oldest first, without their
-- metadata: the page bounds of a paged index.
-- name: ListRegVersionNames :many
SELECT version FROM nuget_versions
WHERE repo_id = ? AND lower_id = ? AND semver2 <= ?
ORDER BY sort_key
LIMIT ?;

-- The versions of one package that a registration variant shows from a sort key through another,
-- oldest first: one page of a paged registration.
-- name: ListRegVersionsBetween :many
SELECT v.package_id, v.version, v.normalized, v.sort_key, v.semver2, v.prerelease, v.listed, v.meta,
  v.nupkg_path, v.published_at, a.size, a.sha256, a.updated_at, a.build_id
FROM nuget_versions v JOIN assets a ON a.id = v.asset_id
WHERE v.repo_id = sqlc.arg(repo_id) AND v.lower_id = sqlc.arg(lower_id) AND v.semver2 <= sqlc.arg(semver2)
  AND v.sort_key >= sqlc.arg(lower) AND v.sort_key <= sqlc.arg(upper)
ORDER BY v.sort_key
LIMIT sqlc.arg(limit);

-- One version of a package.
-- name: GetVersion :one
SELECT v.package_id, v.version, v.normalized, v.sort_key, v.semver2, v.prerelease, v.listed, v.meta,
  v.nupkg_path, v.published_at, a.size, a.sha256, a.updated_at, a.build_id
FROM nuget_versions v JOIN assets a ON a.id = v.asset_id
WHERE v.repo_id = ? AND v.lower_id = ? AND v.sort_key = ?;

-- Whether a repo has a version of a package.
-- name: HasPackage :one
SELECT 1 FROM nuget_versions WHERE repo_id = ? AND lower_id = ? LIMIT 1;

-- Unlist (0) or relist (1) one version.
-- name: SetListed :execrows
UPDATE nuget_versions SET listed = ? WHERE repo_id = ? AND lower_id = ? AND sort_key = ?;


-- A repo delete empties this table batch by batch before its row goes (repos.Service.OnPurge).
-- name: PurgeNugetVersions :execrows
DELETE FROM nuget_versions WHERE id IN (SELECT x.id FROM nuget_versions x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

