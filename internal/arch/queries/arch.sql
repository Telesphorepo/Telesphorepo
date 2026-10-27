-- The arch_* tables (internal/arch). The indexes stream through the hand-written queries of
-- dbq/extra.go.

-- Upload duplicate check: a point lookup on the UNIQUE index.
-- name: GetPackageSHA256 :one
SELECT sha256 FROM arch_packages WHERE repo_id = ? AND name = ? AND version = ? AND arch = ?;

-- name: InsertPackage :one
INSERT INTO arch_packages (repo_id, name, version, arch, vkey, asset_id, filename, size, sha256, sig_state, desc, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: InsertPackageFiles :exec
INSERT INTO arch_package_files (package_id, files) VALUES (?, ?);

-- The package of a pool file, through arch_packages_asset.
-- name: GetPackageOfAsset :one
SELECT id, name, version, arch, filename, size, sha256, desc FROM arch_packages WHERE asset_id = ? LIMIT 1;

-- The package that a name installs in one architecture's index, with its order key.
-- name: GetEntry :one
SELECT p.id, p.vkey, p.arch FROM arch_db_entries e JOIN arch_packages p ON p.id = e.package_id
WHERE e.repo_id = ? AND e.arch = ? AND e.name = ?;

-- name: UpsertEntry :exec
INSERT INTO arch_db_entries (repo_id, arch, name, package_id) VALUES (?, ?, ?, ?)
ON CONFLICT (repo_id, arch, name) DO UPDATE SET package_id = excluded.package_id;

-- name: DeleteEntry :exec
DELETE FROM arch_db_entries WHERE repo_id = ? AND arch = ? AND name = ?;

-- The newest package of a name with architecture arch, through arch_packages_arch.
-- name: NewestOf :one
SELECT id, vkey FROM arch_packages WHERE repo_id = ? AND arch = ? AND name = ?
ORDER BY vkey DESC LIMIT 1;

-- The index entries that name a package, through arch_db_entries_package.
-- name: EntriesOfPackage :many
SELECT arch, name FROM arch_db_entries WHERE package_id = ?;

-- name: DeletePackage :exec
DELETE FROM arch_packages WHERE id = ?;

-- Whether a package name has a file, through arch_packages_order.
-- name: PackageHasFiles :one
SELECT EXISTS (SELECT 1 FROM arch_packages WHERE repo_id = ? AND name = ?);

-- One batch of the entries of an architecture that the config no longer lists, or that a fill
-- writes again.
-- name: DeleteEntriesOfArch :execrows
DELETE FROM arch_db_entries WHERE arch_db_entries.repo_id = sqlc.arg(repo_id) AND arch_db_entries.arch = sqlc.arg(arch) AND arch_db_entries.name IN (
  SELECT x.name FROM arch_db_entries x WHERE x.repo_id = sqlc.arg(repo_id) AND x.arch = sqlc.arg(arch) LIMIT sqlc.arg(limit));

-- A page of the packages of a repo in UNIQUE index order, for the signatures of a key change.
-- name: SignPage :many
SELECT name, version, arch, filename, sig_state FROM arch_packages
WHERE repo_id = sqlc.arg(repo_id) AND (name, version, arch) > (sqlc.arg(name), sqlc.arg(version), sqlc.arg(arch))
ORDER BY name, version, arch LIMIT sqlc.arg(limit);

-- A repo delete empties these tables batch by batch before its row goes (repos.Service.OnPurge).
-- name: PurgeEntries :execrows
DELETE FROM arch_db_entries WHERE arch_db_entries.repo_id = sqlc.arg(repo_id) AND (arch_db_entries.arch, arch_db_entries.name) IN (
  SELECT x.arch, x.name FROM arch_db_entries x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- name: PurgePackages :execrows
DELETE FROM arch_packages WHERE id IN (SELECT x.id FROM arch_packages x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));
