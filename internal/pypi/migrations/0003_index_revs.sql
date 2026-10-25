-- The revision of the project list (/simple/) of a hosted repo or a proxy: it moves on when a
-- project comes or goes or changes its display name (max(the rev before + 1, unix ns of the
-- change)), so the rendered list is kept per revision. A repo without a row is at revision 0.
CREATE TABLE IF NOT EXISTS pypi_index_revs (
  repo_id INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  rev     INTEGER NOT NULL
) STRICT;
