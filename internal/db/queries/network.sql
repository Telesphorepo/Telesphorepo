-- IP range presets and per-repo IP rules (internal/netrules). Request paths read the in-memory
-- copies; these queries run at startup, on a refresh and on an admin change.

-- name: GetPresetRanges :one
SELECT name, ranges, fetched_at, error FROM preset_ranges WHERE name = ?;

-- name: SavePresetRanges :exec
INSERT INTO preset_ranges (name, ranges, fetched_at, error) VALUES (?, ?, ?, '')
ON CONFLICT (name) DO UPDATE SET ranges = excluded.ranges, fetched_at = excluded.fetched_at, error = '';

-- name: SetPresetError :exec
INSERT INTO preset_ranges (name, error) VALUES (?, ?)
ON CONFLICT (name) DO UPDATE SET error = excluded.error;

-- The next repo after repo_id that has IP rules: one seek on the primary key.
-- name: NextIPRuleRepo :one
SELECT repo_id FROM repo_ip_rules WHERE repo_id > ? ORDER BY repo_id LIMIT 1;

-- name: ListRepoIPRules :many
SELECT scope, kind, value, note FROM repo_ip_rules WHERE repo_id = ? ORDER BY scope, kind, value;

-- name: DeleteRepoIPRules :exec
DELETE FROM repo_ip_rules WHERE repo_id = ?;

-- name: InsertRepoIPRule :exec
INSERT INTO repo_ip_rules (repo_id, scope, kind, value, note) VALUES (?, ?, ?, ?, ?);