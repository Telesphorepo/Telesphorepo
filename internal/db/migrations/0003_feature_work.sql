-- 0003_feature_work.sql. db.Init runs this file once, in one transaction with foreign keys off and
-- checked before the commit. Once a release has applied it, never change it: add the next
-- migration instead.

-- The downloads of each repo per day (internal/stats): day is the Unix time of 00:00 UTC of the
-- day. The server adds to counters in memory and writes them once a minute, one upsert per repo.
-- download_stats_day serves the window of GET /api/stats and the retention delete.
CREATE TABLE IF NOT EXISTS download_stats (
  repo_id   INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  day       INTEGER NOT NULL,
  downloads INTEGER NOT NULL DEFAULT 0,
  bytes     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (repo_id, day)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS download_stats_day ON download_stats(day);
