-- In-app settings (internal/settings). Request paths read the in-memory snapshot, never this table.

-- Loads every stored setting into the snapshot at startup and after each write.
-- name: ListSettings :many
SELECT key, value FROM settings;

-- name: DeleteSetting :exec
DELETE FROM settings WHERE key = ?;

-- name: UpsertSetting :exec
INSERT INTO settings (key, value, updated_at, updated_by) VALUES (?, ?, ?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value,
  updated_at = excluded.updated_at, updated_by = excluded.updated_by;
