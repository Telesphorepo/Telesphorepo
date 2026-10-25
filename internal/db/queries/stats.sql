-- The download counters (internal/stats).

-- One flush of a repo's counters of one day. A repo that was deleted since the downloads has no
-- row to add to, so the SELECT finds none and nothing is written.
-- name: AddDownloads :exec
INSERT INTO download_stats (repo_id, day, downloads, bytes)
SELECT r.id, sqlc.arg(day), sqlc.arg(downloads), sqlc.arg(bytes) FROM repos r WHERE r.id = sqlc.arg(repo_id)
ON CONFLICT (repo_id, day) DO UPDATE SET
  downloads = download_stats.downloads + excluded.downloads,
  bytes = download_stats.bytes + excluded.bytes;

-- The window of the last 30 days is kept in three tables (see the triggers of download_stats): the
-- totals of each day, the totals of each repo, and the first day of the window.

-- The first day of the window.
-- name: GetWindowFirst :one
SELECT first_day FROM download_window_state WHERE id = 1;

-- name: SetWindowFirst :exec
UPDATE download_window_state SET first_day = sqlc.arg(first_day) WHERE id = 1;

-- The rows of one day, through download_stats_day: at most one per repo.
-- name: ListDownloadsOfDay :many
SELECT repo_id, downloads, bytes FROM download_stats WHERE day = sqlc.arg(day);

-- A day that left the window: its repo rows are subtracted one by one, the repo rows left empty are
-- deleted, and the day rows before the window go.
-- name: SubWindowRepo :exec
UPDATE download_window_repos SET downloads = downloads - sqlc.arg(downloads), bytes = bytes - sqlc.arg(bytes)
WHERE repo_id = sqlc.arg(repo_id);

-- name: DeleteEmptyWindowRepo :exec
DELETE FROM download_window_repos WHERE repo_id = sqlc.arg(repo_id) AND downloads <= 0;

-- name: DeleteWindowDaysBefore :exec
DELETE FROM download_window_days WHERE day < sqlc.arg(before);

-- The totals of the days from since on, through the primary key: at most one row per day.
-- name: ListWindowDays :many
SELECT day, downloads, bytes FROM download_window_days WHERE day >= sqlc.arg(since) ORDER BY day;

-- The repos with the most downloads in the window (then the most bytes, then the oldest repo), most
-- first, through download_window_repos_top.
-- name: TopWindowRepos :many
SELECT r.name, r.format, w.downloads, w.bytes
FROM download_window_repos w JOIN repos r ON r.id = w.repo_id
WHERE w.downloads > 0
ORDER BY w.downloads DESC, w.bytes DESC, w.repo_id LIMIT sqlc.arg(n);

-- The downloads of one repo in the window; no row for none.
-- name: GetWindowRepoDownloads :one
SELECT downloads FROM download_window_repos WHERE repo_id = sqlc.arg(repo_id);

-- Retention: the days before a cutoff, through download_stats_day.
-- name: DeleteOldDownloads :execrows
DELETE FROM download_stats WHERE day < sqlc.arg(before);


-- The last use of a redirect, written with the download counters. It never moves back.
-- name: TouchRepoRedirect :exec
UPDATE repo_redirects SET last_used_at = max(last_used_at, CAST(sqlc.arg(at) AS INTEGER)) WHERE name = sqlc.arg(name);