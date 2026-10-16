-- Build records (internal/ci).

-- A run that published twice keeps one row: the second mint updates its fields and its time, so the
-- cleanup never deletes a build that a session has just named.
-- name: UpsertBuild :one
INSERT INTO builds (provider, key, repository, revision, ref, ref_name, ref_type, run, workflow, actor, links, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (provider, key) DO UPDATE SET repository = excluded.repository, revision = excluded.revision,
  ref = excluded.ref, ref_name = excluded.ref_name, ref_type = excluded.ref_type, run = excluded.run,
  workflow = excluded.workflow, actor = excluded.actor, links = excluded.links,
  created_at = max(builds.created_at, excluded.created_at)
RETURNING id;

-- name: GetBuild :one
SELECT id, provider, key, repository, revision, ref, ref_name, ref_type, run, workflow, actor, links, created_at
FROM builds WHERE id = ?;

-- A page of the builds created before the cutoff, in id order after the id after (the cleanup
-- walks them with a cursor).
-- name: ListOldBuilds :many
SELECT id FROM builds WHERE id > sqlc.arg(after) AND created_at <= sqlc.arg(cutoff) ORDER BY id LIMIT sqlc.arg(limit);

-- Deletes a build that no file references, through assets_build. A session that names it keeps
-- its row with build_id NULL.
-- name: DeleteUnusedBuild :execrows
DELETE FROM builds WHERE builds.id = sqlc.arg(build) AND NOT EXISTS (SELECT 1 FROM assets a WHERE a.build_id = sqlc.arg(build));
