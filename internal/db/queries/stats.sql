-- The download counters (internal/stats).

-- One flush of the counters in one statement: rows is a JSON array of [repo_id, day, downloads,
-- bytes], one per repo and day. A repo that was deleted since the downloads has no row to add to,
-- so the join drops its counts.
-- name: AddDownloads :exec
INSERT INTO download_stats (repo_id, day, downloads, bytes)
SELECT r.id, json_extract(j.value, '$[1]'), json_extract(j.value, '$[2]'), json_extract(j.value, '$[3]')
FROM json_each(CAST(sqlc.arg(rows) AS TEXT)) j CROSS JOIN repos r
WHERE r.id = json_extract(j.value, '$[0]')
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

-- A day that left the window: the rows of its repos (through download_stats_day, at most one per
-- repo) are subtracted from the repo totals in one statement, each a seek of the download_stats
-- primary key, the repo rows left empty are deleted, and the day rows before the window go.
-- name: SubWindowDay :exec
UPDATE download_window_repos SET
  downloads = downloads - (SELECT s.downloads FROM download_stats s WHERE s.repo_id = download_window_repos.repo_id AND s.day = sqlc.arg(day)),
  bytes = bytes - (SELECT s.bytes FROM download_stats s WHERE s.repo_id = download_window_repos.repo_id AND s.day = sqlc.arg(day))
WHERE repo_id IN (SELECT d.repo_id FROM download_stats d WHERE d.day = sqlc.arg(day));

-- name: DeleteEmptyWindowRepos :exec
DELETE FROM download_window_repos
WHERE downloads <= 0 AND repo_id IN (SELECT d.repo_id FROM download_stats d WHERE d.day = sqlc.arg(day));

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

-- The downloads of the repos of ids in the window (a page of repos), a seek of the primary key per
-- id in one query; a repo without downloads has no row.
-- name: ListWindowRepoDownloads :many
SELECT repo_id, downloads FROM download_window_repos WHERE repo_id IN (sqlc.slice(ids));

-- Retention: the days before a cutoff, through download_stats_day.
-- name: DeleteOldDownloads :execrows
DELETE FROM download_stats WHERE day < sqlc.arg(before);


-- The last use of the redirects, written with the download counters in one statement: uses is a
-- JSON object of redirect name to Unix time, a seek of the primary key per name. A use never
-- moves back.
-- name: TouchRepoRedirects :exec
UPDATE repo_redirects SET last_used_at = max(last_used_at, CAST(json_extract(CAST(sqlc.arg(uses) AS TEXT), '$."' || name || '"') AS INTEGER))
WHERE name IN (SELECT j.key FROM json_each(CAST(sqlc.arg(uses) AS TEXT)) j);