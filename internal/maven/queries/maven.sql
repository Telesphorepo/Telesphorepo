-- The maven_* tables (internal/maven).

-- Records a build; 1 when it is new.
-- name: InsertBuild :execrows
INSERT OR IGNORE INTO maven_builds (repo_id, folder, sort_key, build) VALUES (?, ?, ?, ?);

-- Counts a new build of a version, recording the version with its first build.
-- name: AddVersionBuild :exec
INSERT INTO maven_versions (repo_id, folder, sort_key, version, snapshot, builds) VALUES (?, ?, ?, ?, ?, 1)
ON CONFLICT (repo_id, folder, sort_key) DO UPDATE SET builds = maven_versions.builds + 1;

-- Deletes a build that has no file left; 1 when it was there.
-- name: DeleteBuild :execrows
DELETE FROM maven_builds WHERE repo_id = ? AND folder = ? AND sort_key = ?;

-- name: DropVersionBuild :exec
UPDATE maven_versions SET builds = builds - 1 WHERE repo_id = ? AND folder = ? AND sort_key = ?;

-- name: DeleteEmptyVersion :exec
DELETE FROM maven_versions WHERE repo_id = ? AND folder = ? AND sort_key = ? AND builds <= 0;

-- A page of the versions of an artifact folder, newest first, below the sort key of the cursor,
-- through the primary key.
-- name: ListVersionsBelow :many
SELECT version, builds FROM maven_versions
WHERE repo_id = ? AND folder = ? AND sort_key < ?
ORDER BY sort_key DESC
LIMIT ?;

-- One version of an artifact folder, through the primary key.
-- name: GetVersion :one
SELECT version, builds FROM maven_versions WHERE repo_id = ? AND folder = ? AND sort_key = ?;

-- The newest release (snapshot 0) or snapshot (1) of an artifact folder, through
-- maven_versions_latest.
-- name: LatestVersion :one
SELECT version FROM maven_versions
WHERE repo_id = ? AND folder = ? AND snapshot = ?
ORDER BY sort_key DESC
LIMIT 1;

-- A page of the builds of a version folder, newest first, below the sort key of the cursor,
-- through the primary key.
-- name: ListBuildsBelow :many
SELECT build FROM maven_builds
WHERE repo_id = ? AND folder = ? AND sort_key < ?
ORDER BY sort_key DESC
LIMIT ?;

-- One build of a version folder, through the primary key.
-- name: GetBuild :one
SELECT build FROM maven_builds WHERE repo_id = ? AND folder = ? AND sort_key = ?;

-- The paths of a repo in [lower, upper), in path order, through the assets (repo_id, path) key:
-- the files of a build in the transaction of a remove.
-- name: ListPathsInRange :many
SELECT path FROM assets
WHERE repo_id = sqlc.arg(repo_id) AND path >= sqlc.arg(lower) AND path < sqlc.arg(upper)
ORDER BY path
LIMIT sqlc.arg(limit);

-- The paths of a repo after a path, in path order: the files that the index of the files a repo
-- holds already reads, batch by batch.
-- name: ListPathsAfter :many
SELECT path FROM assets
WHERE repo_id = ? AND path > ?
ORDER BY path
LIMIT ?;

-- name: IndexPending :one
SELECT count(*) FROM maven_index_pending WHERE one = 1;

-- name: IndexDone :exec
DELETE FROM maven_index_pending WHERE one = 1;

-- A repo delete empties these tables batch by batch before its row goes (repos.Service.OnPurge).
-- name: PurgeMavenBuilds :execrows
DELETE FROM maven_builds WHERE maven_builds.repo_id = sqlc.arg(repo_id) AND (folder, sort_key) IN
  (SELECT x.folder, x.sort_key FROM maven_builds x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- name: PurgeMavenVersions :execrows
DELETE FROM maven_versions WHERE maven_versions.repo_id = sqlc.arg(repo_id) AND (folder, sort_key) IN
  (SELECT x.folder, x.sort_key FROM maven_versions x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));
