-- 0004_repo_redirects.sql. db.Init runs this file once, in one transaction with foreign keys off
-- and checked before the commit. Once a release has applied it, never change it: add the next
-- migration instead.

-- Repository redirects (internal/repos): a name that answers for the repo repo_id. A rename leaves
-- the old name here. Resolving a name reads repos by its UNIQUE name first, then this table by its
-- key. A redirect holds the repo id, so a later rename of the repo keeps it pointing at the repo.
-- last_used_at is the unix time of the last request through the name (0 for never); the server
-- keeps it in memory and writes it with the download counters, once a minute (internal/stats).
-- repo_redirects_repo lists the redirects of a repo (its settings Overview, the edge objects).
CREATE TABLE IF NOT EXISTS repo_redirects (
  name         TEXT PRIMARY KEY,
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS repo_redirects_repo ON repo_redirects(repo_id, name);
