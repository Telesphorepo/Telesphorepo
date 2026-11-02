-- IP lists and the allowed networks of the repos (internal/netrules). Request paths read the in-memory copies;
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

-- The repos whose allowed networks name a list, through repo_networks_list_id.
-- name: ListReposOfList :many
SELECT r.name FROM repo_networks x JOIN repos r ON r.id = x.repo_id
WHERE x.list_id = ?;

-- The next repo after repo_id that has allowed networks: one seek on repo_networks_repo.
-- name: NextNetworkRepo :one
SELECT repo_id FROM repo_networks WHERE repo_id > ? ORDER BY repo_id LIMIT 1;

-- The allowed networks of a repo in entry order, through repo_networks_repo.
-- name: ListRepoNetworks :many
SELECT id, cidr, list_id, note FROM repo_networks WHERE repo_id = ? ORDER BY id;

-- Whether a repo has allowed networks, through repo_networks_repo.
-- name: HasRepoNetworks :one
SELECT EXISTS (SELECT 1 FROM repo_networks WHERE repo_id = ?);

-- name: DeleteRepoNetworks :exec
DELETE FROM repo_networks WHERE repo_id = ?;

-- name: InsertRepoNetwork :exec
INSERT INTO repo_networks (repo_id, cidr, list_id, note) VALUES (?, ?, ?, ?);

-- Whether a repo is public, read in the transaction that saves its allowed networks.
-- name: RepoIsPublic :one
SELECT anonymous_read FROM repos WHERE id = ?;

-- The IP lists that trusted proxies include, by list id.
-- name: ListTrustedProxyLists :many
SELECT list_id FROM trusted_proxy_lists ORDER BY list_id;

-- name: DeleteTrustedProxyLists :exec
DELETE FROM trusted_proxy_lists;

-- name: InsertTrustedProxyList :exec
INSERT INTO trusted_proxy_lists (list_id) VALUES (?);