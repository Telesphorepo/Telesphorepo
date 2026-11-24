-- IP lists and the allowed networks of the repos (internal/netrules). Request paths read the in-memory copies;
-- these queries run at startup, on a refresh and on an admin change.

-- Keyset page by name (pass "" for the first page).
-- name: ListIPLists :many
SELECT id, name, kind, title, description, ranges, source, fetched_at, error, created_at
FROM ip_lists WHERE name > sqlc.arg(after) ORDER BY name LIMIT sqlc.arg(limit);

-- A list by name: the row that a preset's start wrote, for its grams.
-- name: GetIPListName :one
SELECT id, name, title FROM ip_lists WHERE name = ?;

-- The search of the lists' names and titles (internal/namesearch, migration
-- 0003_list_name_search.sql): the lists in the rows of one gram whose name or title holds the text.
-- name: SearchIPLists :many
SELECT list_id FROM ip_list_grams WHERE gram = sqlc.arg(gram) AND instr(text, sqlc.arg(text)) > 0;

-- The probe of a gram: its rows, up to limit.
-- name: CountIPListGram :one
SELECT count(*) FROM (SELECT 1 FROM ip_list_grams WHERE gram = sqlc.arg(gram) LIMIT sqlc.arg(limit));

-- The grams of a list in one statement: grams is a JSON array of the grams.
-- name: InsertIPListGrams :exec
INSERT OR REPLACE INTO ip_list_grams (gram, list_id, text)
SELECT g.value, CAST(sqlc.arg(list_id) AS INTEGER), CAST(sqlc.arg(text) AS TEXT) FROM json_each(sqlc.arg(grams)) g;

-- The grams of a list, through ip_list_grams_list (a change of its name or title writes them again).
-- name: DeleteIPListGrams :exec
DELETE FROM ip_list_grams WHERE list_id = ?;

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

-- The repos whose allowed networks name each of the lists of a JSON array of list ids, through
-- repo_networks_list_id: one query for every list of GET /api/ip-lists.
-- name: ListReposOfLists :many
SELECT x.list_id, r.name FROM repo_networks x JOIN repos r ON r.id = x.repo_id
WHERE x.list_id IN (SELECT value FROM json_each(sqlc.arg(list_ids)));

-- A page of every allowed network in id order (entry order within a repo), after the id after:
-- the start reads them all into memory, a page per query.
-- name: ListNetworksAfter :many
SELECT id, repo_id, cidr, list_id, note FROM repo_networks WHERE id > sqlc.arg(after) ORDER BY id LIMIT sqlc.arg(limit);

-- The public repos among a JSON array of repo ids (the repos with allowed networks): one seek
-- of the primary key per id.
-- name: ListPublicReposIn :many
SELECT id, name FROM repos WHERE id IN (SELECT value FROM json_each(sqlc.arg(ids))) AND anonymous_read = 1;

-- Whether a repo has allowed networks, through repo_networks_repo.
-- name: HasRepoNetworks :one
SELECT EXISTS (SELECT 1 FROM repo_networks WHERE repo_id = ?);

-- name: DeleteRepoNetworks :exec
DELETE FROM repo_networks WHERE repo_id = ?;

-- The allowed networks of a repo in one statement, in entry order: networks is a JSON array of
-- {"cidr", "list_id", "note"} (cidr or list_id null).
-- name: InsertRepoNetworks :exec
INSERT INTO repo_networks (repo_id, cidr, list_id, note)
SELECT CAST(sqlc.arg(repo_id) AS INTEGER), json_extract(n.value, '$.cidr'), json_extract(n.value, '$.list_id'),
  json_extract(n.value, '$.note')
FROM json_each(sqlc.arg(networks)) n;

-- Whether a repo is public, read in the transaction that saves its allowed networks.
-- name: RepoIsPublic :one
SELECT anonymous_read FROM repos WHERE id = ?;

-- The IP lists that trusted proxies include, by list id.
-- name: ListTrustedProxyLists :many
SELECT list_id FROM trusted_proxy_lists ORDER BY list_id;

-- name: DeleteTrustedProxyLists :exec
DELETE FROM trusted_proxy_lists;

-- The IP lists that trusted proxies include, in one statement: list_ids is a JSON array.
-- name: InsertTrustedProxyLists :exec
INSERT INTO trusted_proxy_lists (list_id) SELECT value FROM json_each(sqlc.arg(list_ids));