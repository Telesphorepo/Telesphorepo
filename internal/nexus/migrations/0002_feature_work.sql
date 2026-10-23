-- 0002_feature_work.sql. db.Init runs this file once, in one transaction with foreign keys off and
-- checked before the commit. Every statement can run twice. Once a release has applied it, never
-- change it: add the next migration instead.

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

-- Repositories that projects move to one by one: repo_id is the new hosted repo, into_repo_id the
-- repo that a Nexus source copies into, which it merges into when the last project has moved.
-- repo_moves_into lists the moving repos of a repo (the Nexus page, the purge of a deleted repo).
CREATE TABLE IF NOT EXISTS repo_moves (
  repo_id      INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  into_repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS repo_moves_into ON repo_moves(into_repo_id);

-- The merges of one hosted repo (the source, which the merge deletes, so it has no foreign key)
-- into a repo that a Nexus source copies into (the target). state is running, failed, done or
-- stopped; step is the step it is at or ended after: sync (the final sync, sync_id), copy (the
-- files after cursor, by path), access, switch, retire, rename, done. keep names the repo whose
-- name the merged repo keeps (target or source); otherwise is the choice for a conflict that the
-- choices do not name ("" refuses it, keep or take). The counters count the files of the source:
-- copied, skipped (the same bytes in the target), rebuilt (files the format builds or merges),
-- kept (a conflict that keeps the target's file) and taken (a conflict that takes the source's).
-- files is the source's file count at the start, done the files walked so far.
-- nexus_merges_target lists the merges of a target, newest first; nexus_merges_sync finds the
-- merge that waits for the end of its final sync.
CREATE TABLE IF NOT EXISTS nexus_merges (
  id             INTEGER PRIMARY KEY,
  target_repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  source_repo_id INTEGER NOT NULL,
  source_name    TEXT NOT NULL,
  target_name    TEXT NOT NULL,
  keep           TEXT NOT NULL,
  otherwise      TEXT NOT NULL DEFAULT '',
  state          TEXT NOT NULL,
  step           TEXT NOT NULL,
  sync_id        INTEGER NOT NULL DEFAULT 0,
  cursor         TEXT NOT NULL DEFAULT '',
  files          INTEGER NOT NULL DEFAULT 0,
  done           INTEGER NOT NULL DEFAULT 0,
  copied         INTEGER NOT NULL DEFAULT 0,
  skipped        INTEGER NOT NULL DEFAULT 0,
  rebuilt        INTEGER NOT NULL DEFAULT 0,
  kept           INTEGER NOT NULL DEFAULT 0,
  taken          INTEGER NOT NULL DEFAULT 0,
  error          TEXT NOT NULL DEFAULT '',
  started_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
  started_at     INTEGER NOT NULL,
  finished_at    INTEGER
) STRICT;
CREATE INDEX IF NOT EXISTS nexus_merges_target ON nexus_merges(target_repo_id, id);
CREATE INDEX IF NOT EXISTS nexus_merges_sync ON nexus_merges(sync_id);
CREATE INDEX IF NOT EXISTS nexus_merges_started_by ON nexus_merges(started_by);

-- The choice of each conflict that the plan of a merge listed: take 1 deletes the target's file
-- and takes the source's, take 0 keeps the target's.
CREATE TABLE IF NOT EXISTS nexus_merge_choices (
  merge_id INTEGER NOT NULL REFERENCES nexus_merges(id) ON DELETE CASCADE,
  path     TEXT NOT NULL,
  take     INTEGER NOT NULL,
  PRIMARY KEY (merge_id, path)
) STRICT, WITHOUT ROWID;
