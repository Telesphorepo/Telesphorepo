-- 0002_switch_over.sql. db.Init runs this file once, in one transaction with foreign keys off and
-- checked before the commit. Once a release has applied it, never change it: add the next
-- migration instead.

-- The mapped hosted repos of a source that switched over: the final sync of the repo (sync_id),
-- the files it failed to import (failed; listed under that sync) and when the repo switched over.
-- A mapped hosted repo without a row here still copies from Nexus. The source's state follows:
-- when no mapped hosted repo copies any more, the source is cut over. The row goes with its
-- mapping.
CREATE TABLE IF NOT EXISTS nexus_switch_overs (
  source_id   INTEGER NOT NULL,
  source_repo TEXT NOT NULL,
  sync_id     INTEGER NOT NULL,
  failed      INTEGER NOT NULL DEFAULT 0,
  switched_at INTEGER NOT NULL,
  PRIMARY KEY (source_id, source_repo),
  FOREIGN KEY (source_id, source_repo) REFERENCES nexus_source_repos(source_id, source_repo) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- Rehearsal repositories: repo_id rehearses the publishing of for_repo_id, a hosted repo that a
-- Nexus source copies into. repo_rehearsals_for lists the rehearsals of a repo (the Nexus page,
-- the purge of a deleted repo).
CREATE TABLE IF NOT EXISTS repo_rehearsals (
  repo_id     INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  for_repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS repo_rehearsals_for ON repo_rehearsals(for_repo_id);
