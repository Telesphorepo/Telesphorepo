-- Effective access (internal/access): who can download from and publish to a repo, and what a
-- user can do in each repo. Admin pages only; every query is bounded by one repo or one user.

-- The grants of one repo with their users, through the repo_permissions primary key. The caller
-- sorts them.
-- name: ListRepoGrants :many
SELECT u.username, u.is_admin, u.disabled, u.kind, p.level
FROM repo_permissions p JOIN users u ON u.id = p.user_id
WHERE p.repo_id = ?;

-- The enabled admins in username order, through users_admins.
-- name: ListActiveAdmins :many
SELECT username, kind FROM users WHERE is_admin = 1 AND disabled = 0 ORDER BY username;

-- The CI rules (trust policies) that list one repo, through publish_trust_repos_repo. The caller
-- sorts them.
-- name: ListRepoTrusts :many
SELECT t.id, t.name, t.issuer, t.claims, t.disabled
FROM publish_trust_repos tr JOIN publish_trusts t ON t.id = tr.trust_id
WHERE tr.repo_id = ?;

-- The grants of one user with their repos, through repo_permissions_user.
-- name: ListUserGrants :many
SELECT p.repo_id, p.level FROM repo_permissions p WHERE p.user_id = ?;
