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
