-- 0006_stats_window_public_name.sql. db.Init runs this file once. Every statement can run twice.

-- The window of GET /api/stats sums downloads and bytes by day and by repo from this index alone:
-- it holds every column of the sums, in day order, so the sums never read the table. It replaces
-- download_stats_day; the retention delete seeks through it too.
DROP INDEX IF EXISTS download_stats_day;
CREATE INDEX IF NOT EXISTS download_stats_window ON download_stats(day, repo_id, downloads, bytes);

-- A user's access (internal/access) reads the public repos of a page in name order through this
-- index.
CREATE INDEX IF NOT EXISTS repos_anonymous_name ON repos(name) WHERE anonymous_read = 1;
