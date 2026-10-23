-- The download counters (internal/stats).

-- One flush of a repo's counters of one day. A repo that was deleted since the downloads has no
-- row to add to, so the SELECT finds none and nothing is written.
-- name: AddDownloads :exec
INSERT INTO download_stats (repo_id, day, downloads, bytes)
SELECT r.id, sqlc.arg(day), sqlc.arg(downloads), sqlc.arg(bytes) FROM repos r WHERE r.id = sqlc.arg(repo_id)
ON CONFLICT (repo_id, day) DO UPDATE SET
  downloads = download_stats.downloads + excluded.downloads,
  bytes = download_stats.bytes + excluded.bytes;

-- The rows of the days from since on, through download_stats_day: at most one per repo and day.
-- name: ListDownloadsSince :many
SELECT repo_id, day, downloads, bytes FROM download_stats WHERE day >= sqlc.arg(since) ORDER BY day;

-- Retention: the days before a cutoff, through download_stats_day.
-- name: DeleteOldDownloads :execrows
DELETE FROM download_stats WHERE day < sqlc.arg(before);
