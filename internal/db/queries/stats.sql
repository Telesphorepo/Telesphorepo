-- The download counters (internal/stats).

-- One flush of a repo's counters of one day. A repo that was deleted since the downloads has no
-- row to add to, so the SELECT finds none and nothing is written.
-- name: AddDownloads :exec
INSERT INTO download_stats (repo_id, day, downloads, bytes)
SELECT r.id, sqlc.arg(day), sqlc.arg(downloads), sqlc.arg(bytes) FROM repos r WHERE r.id = sqlc.arg(repo_id)
ON CONFLICT (repo_id, day) DO UPDATE SET
  downloads = download_stats.downloads + excluded.downloads,
  bytes = download_stats.bytes + excluded.bytes;

-- The rows of the days from since on, through download_stats_window: at most one per repo and day.
-- name: ListDownloadsSince :many
SELECT repo_id, day, downloads, bytes FROM download_stats WHERE day >= sqlc.arg(since) ORDER BY day;

-- The downloads of each day from since on, through download_stats_window (covering, in day order).
-- name: SumDownloadsByDay :many
SELECT day, CAST(SUM(downloads) AS INTEGER) AS downloads, CAST(SUM(bytes) AS INTEGER) AS bytes
FROM download_stats WHERE day >= sqlc.arg(since) GROUP BY day ORDER BY day;

-- The repos with the most downloads from since on (then the most bytes, then the oldest repo), most
-- first: the window is summed per repo from download_stats_window alone, and only the top ones are
-- joined to repos.
-- name: TopDownloadRepos :many
SELECT r.name, r.format, t.downloads, t.bytes FROM (
  SELECT repo_id, CAST(SUM(downloads) AS INTEGER) AS downloads, CAST(SUM(bytes) AS INTEGER) AS bytes
  FROM download_stats WHERE day >= sqlc.arg(since)
  GROUP BY repo_id ORDER BY 2 DESC, 3 DESC, repo_id LIMIT sqlc.arg(n)
) t JOIN repos r ON r.id = t.repo_id
ORDER BY t.downloads DESC, t.bytes DESC, r.id;

-- Retention: the days before a cutoff, through download_stats_window.
-- name: DeleteOldDownloads :execrows
DELETE FROM download_stats WHERE day < sqlc.arg(before);


-- The last use of a redirect, written with the download counters. It never moves back.
-- name: TouchRepoRedirect :exec
UPDATE repo_redirects SET last_used_at = max(last_used_at, CAST(sqlc.arg(at) AS INTEGER)) WHERE name = sqlc.arg(name);