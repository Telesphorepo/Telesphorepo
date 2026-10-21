-- The pypi_* tables (internal/pypi).

-- name: UpsertProject :exec
INSERT INTO pypi_projects (repo_id, project, display_name) VALUES (?, ?, ?)
ON CONFLICT (repo_id, project) DO UPDATE SET display_name = excluded.display_name;

-- name: InsertFile :exec
INSERT INTO pypi_files (repo_id, project, version, sort_key, filename, path, asset_id, sha256, size,
  requires_python, metadata_sha256, uploaded_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- A project of a hosted repo: a point lookup on the primary key.
-- name: GetProject :one
SELECT display_name FROM pypi_projects WHERE repo_id = ? AND project = ?;

-- The projects of a hosted repo for the project list, in primary key order.
-- name: ListProjects :many
SELECT project, display_name FROM pypi_projects WHERE repo_id = ? ORDER BY project;

-- The files of one project for its page, through pypi_files_project.
-- name: ListProjectFiles :many
SELECT version, sort_key, filename, sha256, size, requires_python, metadata_sha256, uploaded_at
FROM pypi_files
WHERE repo_id = ? AND project = ?
ORDER BY sort_key;

-- The newest versions of a project for the version browser, through pypi_files_project.
-- name: ListVersions :many
SELECT sort_key, CAST(MIN(version) AS TEXT) AS version, COUNT(*) AS files, CAST(MAX(uploaded_at) AS INTEGER) AS uploaded_at
FROM pypi_files
WHERE repo_id = ? AND project = ?
GROUP BY sort_key
ORDER BY sort_key DESC
LIMIT ?;

-- The versions of a project older than a cursor (keyset), through pypi_files_project.
-- name: ListVersionsBefore :many
SELECT sort_key, CAST(MIN(version) AS TEXT) AS version, COUNT(*) AS files, CAST(MAX(uploaded_at) AS INTEGER) AS uploaded_at
FROM pypi_files
WHERE repo_id = ? AND project = ? AND sort_key < ?
GROUP BY sort_key
ORDER BY sort_key DESC
LIMIT ?;

-- The files of one version with the time and the publish session of each.
-- name: ListVersionFiles :many
SELECT f.version, f.filename, f.path, f.sha256, f.size, a.updated_at, a.build_id
FROM pypi_files f JOIN assets a ON a.id = f.asset_id
WHERE f.repo_id = ? AND f.project = ? AND f.sort_key = ?;

-- The file row of a filename, through the UNIQUE index.
-- name: GetFile :one
SELECT project, version, filename FROM pypi_files WHERE repo_id = ? AND filename = ?;

-- Removes the file rows of a deleted asset, through pypi_files_asset.
-- name: DeleteFilesOfAsset :many
DELETE FROM pypi_files WHERE asset_id = ? RETURNING project;

-- A deleted .metadata sibling: the file no longer advertises it.
-- name: ClearMetadata :exec
UPDATE pypi_files SET metadata_sha256 = '' WHERE repo_id = ? AND filename = ?;

-- Whether a project has a file left, through pypi_files_project.
-- name: ProjectHasFiles :one
SELECT EXISTS (SELECT 1 FROM pypi_files WHERE repo_id = ? AND project = ?);

-- name: DeleteProject :exec
DELETE FROM pypi_projects WHERE pypi_projects.repo_id = ? AND project = ?;

-- A repo delete empties these tables batch by batch before its row goes (repos.Service.OnPurge).
-- name: PurgePypiFiles :execrows
DELETE FROM pypi_files WHERE id IN (SELECT x.id FROM pypi_files x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- name: PurgePypiProjects :execrows
DELETE FROM pypi_projects WHERE pypi_projects.repo_id = sqlc.arg(repo_id) AND project IN (SELECT x.project FROM pypi_projects x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

