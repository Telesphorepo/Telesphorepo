-- IP lists and per-repo IP rules (internal/netrules). Request paths read the in-memory copies;
-- these queries run at startup, on a refresh and on an admin change.

-- Keyset page by name (pass "" for the first page).
-- name: ListIPLists :many
SELECT id, name, kind, title, description, ranges, source, fetched_at, error, created_at
FROM ip_lists WHERE name > sqlc.arg(after) ORDER BY name LIMIT sqlc.arg(limit);

-- A preset gets its row on the first start; later starts update its title, description and
-- source.
-- name: EnsurePresetList :exec
INSERT INTO ip_lists (name, kind, title, description, source, created_at) VALUES (?, 'preset', ?, ?, ?, ?)
ON CONFLICT (name) DO UPDATE SET title = excluded.title, description = excluded.description, source = excluded.source
WHERE ip_lists.kind = 'preset';

-- name: SavePresetRanges :exec
UPDATE ip_lists SET ranges = ?, fetched_at = ?, error = '' WHERE name = ? AND kind = 'preset';

-- name: SetPresetError :exec
UPDATE ip_lists SET error = ? WHERE name = ? AND kind = 'preset';

-- name: CreateCustomList :one
INSERT INTO ip_lists (name, kind, title, description, ranges, created_at) VALUES (?, 'custom', ?, ?, ?, ?)
RETURNING id;

-- name: UpdateCustomList :exec
UPDATE ip_lists SET name = ?, title = ?, description = ?, ranges = ? WHERE id = ? AND kind = 'custom';

-- name: DeleteCustomList :exec
DELETE FROM ip_lists WHERE id = ? AND kind = 'custom';

-- The rules that name a list, with their repos, through repo_ip_rules_list_id.
-- name: ListRulesOfList :many
SELECT r.name AS repo, x.scope FROM repo_ip_rules x JOIN repos r ON r.id = x.repo_id
WHERE x.list_id = ?;

-- The next repo after repo_id that has IP rules: one seek on repo_ip_rules_repo.
-- name: NextIPRuleRepo :one
SELECT repo_id FROM repo_ip_rules WHERE repo_id > ? ORDER BY repo_id LIMIT 1;

-- The rules of a repo in scope and entry order, through repo_ip_rules_repo.
-- name: ListRepoIPRules :many
SELECT id, scope, cidr, list_id, note FROM repo_ip_rules WHERE repo_id = ? ORDER BY scope, id;

-- name: DeleteRepoIPRules :exec
DELETE FROM repo_ip_rules WHERE repo_id = ?;

-- name: InsertRepoIPRule :exec
INSERT INTO repo_ip_rules (repo_id, scope, cidr, list_id, note) VALUES (?, ?, ?, ?, ?);

-- The IP lists that trusted proxies include, by list id.
-- name: ListTrustedProxyLists :many
SELECT list_id FROM trusted_proxy_lists ORDER BY list_id;

-- name: DeleteTrustedProxyLists :exec
DELETE FROM trusted_proxy_lists;

-- name: InsertTrustedProxyList :exec
INSERT INTO trusted_proxy_lists (list_id) VALUES (?);