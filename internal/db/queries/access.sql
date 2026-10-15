-- Effective access (internal/access): who can download from and publish to a repo, and what a
-- person can do in each repo. Every query is bounded by one repo, one user or one permission.

-- The grants of one repo with their users, one row per permission, through the
-- repo_permissions primary key. The caller groups and sorts them.
-- name: ListRepoGrants :many
SELECT u.username, u.disabled, u.kind, p.perm,
  EXISTS (SELECT 1 FROM user_permissions up WHERE up.user_id = u.id AND up.perm = 'repos') AS all_repos
FROM repo_permissions p JOIN users u ON u.id = p.user_id
WHERE p.repo_id = ?;

-- The CI rules (trust policies) that list one repo, through publish_trust_repos_repo. The caller
-- sorts them.
-- name: ListRepoTrusts :many
SELECT t.id, t.name, t.issuer, t.claims, t.disabled
FROM publish_trust_repos tr JOIN publish_trusts t ON t.id = tr.trust_id
WHERE tr.repo_id = ?;

-- The grants of one user with their repos, one row per permission, through
-- repo_permissions_user.
-- name: ListUserGrants :many
SELECT p.repo_id, p.perm FROM repo_permissions p WHERE p.user_id = ?;
