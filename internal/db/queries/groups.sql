-- The counters of group repos (internal/api/groupcounts.go): group_counts, group_count_members
-- and group_count_pending. The triggers of migration 0003 keep a row from its insert on.

-- The counters of a group for a set of members, through UNIQUE (group_id, members), once the job
-- has counted them (no group_count_pending entry).
-- name: GetGroupCount :one
SELECT file_count, total_size, last_published_at FROM group_counts
WHERE group_id = ? AND members = ?
  AND NOT EXISTS (SELECT 1 FROM group_count_pending p WHERE p.count_id = group_counts.id);

-- The row of a group for a set of members, counted or not, and whether the job still counts it.
-- name: FindGroupCount :one
SELECT id, CAST(EXISTS (SELECT 1 FROM group_count_pending p WHERE p.count_id = group_counts.id) AS INTEGER) AS pending
FROM group_counts WHERE group_id = ? AND members = ?;

-- name: InsertGroupCount :one
INSERT INTO group_counts (group_id, members) VALUES (?, ?) RETURNING id;

-- name: InsertGroupCountMember :exec
INSERT INTO group_count_members (count_id, position, member_id) VALUES (?, ?, ?);

-- name: InsertGroupCountPending :exec
INSERT INTO group_count_pending (count_id) VALUES (?);

-- name: DeleteGroupCount :exec
DELETE FROM group_counts WHERE id = ?;

-- Sets the counters of a pending row to zero: from here on they hold what the triggers add.
-- name: ResetGroupCount :execrows
UPDATE group_counts SET file_count = 0, total_size = 0, last_published_at = 0 WHERE id = ?;

-- The members of a row: the first read of the count's snapshot.
-- name: CountGroupCountMembers :one
SELECT CAST(count(*) AS INTEGER) FROM group_count_members WHERE count_id = ?;

-- Adds the count of the snapshot to what the triggers added since.
-- name: AddGroupCount :execrows
UPDATE group_counts SET file_count = file_count + sqlc.arg(file_count), total_size = total_size + sqlc.arg(total_size),
  last_published_at = max(last_published_at, CAST(sqlc.arg(last_published_at) AS INTEGER))
WHERE id = sqlc.arg(id);

-- name: DeleteGroupCountPending :exec
DELETE FROM group_count_pending WHERE count_id = ?;

-- The files of the members of a count that a group serves path by path (unit ''), each from the
-- first member that has its path. It reads every such file of the members once, in a read
-- snapshot: the count runs in the background, once per group and set of members.
-- name: CountGroupPaths :one
SELECT CAST(count(*) AS INTEGER) AS files, CAST(coalesce(sum(a.size), 0) AS INTEGER) AS size,
  CAST(coalesce(max(CASE WHEN a.kind <> 'index' THEN a.updated_at END), 0) AS INTEGER) AS newest
FROM group_count_members gm JOIN assets a ON a.repo_id = gm.member_id
WHERE gm.count_id = sqlc.arg(count_id) AND a.unit = ''
  AND NOT EXISTS (
    SELECT 1 FROM group_count_members e JOIN assets b ON b.repo_id = e.member_id AND b.path = a.path
    WHERE e.count_id = gm.count_id AND e.position < gm.position);

-- The files with a unit of the members of a count that a group serves: those of the member that
-- owns the unit, the first one that has a file of it.
-- name: CountGroupUnits :one
SELECT CAST(count(*) AS INTEGER) AS files, CAST(coalesce(sum(a.size), 0) AS INTEGER) AS size,
  CAST(coalesce(max(CASE WHEN a.kind <> 'index' THEN a.updated_at END), 0) AS INTEGER) AS newest
FROM group_count_members gm JOIN assets a ON a.repo_id = gm.member_id AND a.unit <> ''
WHERE gm.count_id = sqlc.arg(count_id)
  AND NOT EXISTS (
    SELECT 1 FROM group_count_members e JOIN assets b ON b.repo_id = e.member_id AND b.unit = a.unit AND b.unit <> ''
    WHERE e.count_id = gm.count_id AND e.position < gm.position);

-- The counts that hold member: a backfill of the member's units makes them stale.
-- name: DeleteGroupCountsOfMember :exec
DELETE FROM group_counts WHERE id IN (SELECT count_id FROM group_count_members WHERE member_id = ?);

-- The asset rows whose unit is not set, through assets_unit_unset, with the format of their repo.
-- name: ListUnsetUnits :many
SELECT a.id, a.repo_id, a.path, r.format FROM assets a JOIN repos r ON r.id = a.repo_id
WHERE a.unit IS NULL LIMIT ?;

-- name: SetAssetUnit :exec
UPDATE assets SET unit = ? WHERE id = ?;
