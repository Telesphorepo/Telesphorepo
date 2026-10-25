-- Effective access (internal/access): who can download from and publish to a repo, and what a
-- person can do in each repo. Every query is bounded by one repo, one user or one permission.

-- The grants of one repo with their users and their server permissions, one row per permission,
-- through the repo_permissions primary key. The caller groups and sorts them.
-- name: ListRepoGrants :many
SELECT u.username, u.disabled, u.kind, p.perm,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = u.id), '') AS TEXT) AS server_perms
FROM repo_permissions p JOIN users u ON u.id = p.user_id
WHERE p.repo_id = ?;


-- The grants of one user with their repos, one row per permission, through
-- repo_permissions_user.
-- name: ListUserGrants :many
SELECT p.repo_id, p.perm FROM repo_permissions p WHERE p.user_id = ?;


-- The grants of one user with the names of their repos, one row per permission, through
-- repo_permissions_user and the repos primary key.
-- name: ListUserGrantsNamed :many
SELECT r.name, p.perm FROM repo_permissions p JOIN repos r ON r.id = p.repo_id WHERE p.user_id = ?;

-- A keyset page by name of the repos a user can see: the repos they hold a grant on and the public
-- ones. Each side reads at most one page after the cursor (the public side in name order through
-- repos_anonymous_name, the grant side from the user's own grants), and the page is the first
-- rows of the two merged. The columns are those of ListRepos.
-- name: ListReposVisibleTo :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM (
  SELECT pub.id FROM (
    SELECT x.id FROM repos x WHERE x.anonymous_read = 1 AND x.name > sqlc.arg(after) ORDER BY x.name LIMIT sqlc.arg(limit)
  ) pub
  UNION
  SELECT own.id FROM (
    SELECT y.id FROM repos y WHERE y.name > sqlc.arg(after)
      AND y.id IN (SELECT g.repo_id FROM repo_permissions g WHERE g.user_id = sqlc.arg(user_id))
    ORDER BY y.name LIMIT sqlc.arg(limit)
  ) own
) v
JOIN repos r ON r.id = v.id LEFT JOIN repo_ops o ON o.repo_id = r.id
ORDER BY r.name LIMIT sqlc.arg(limit);
