-- Nexus module schema: sources, the repos they mirror and their syncs. References users and repos
-- of the kernel schema.

-- Nexus sources. credentials is the sealed JSON {username, password}
-- (settings.Service.Encrypt, AAD "nexus_sources:<id>"); cut-over empties it. last_sync_at is
-- when the last sync ended (0: never); the scheduler finds the due mirroring sources through
-- nexus_sources_due.
CREATE TABLE nexus_sources (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  base_url     TEXT NOT NULL,
  credentials  BLOB NOT NULL,
  state        TEXT NOT NULL DEFAULT 'mirroring' CHECK (state IN ('mirroring','cut_over')),
  created_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at   INTEGER NOT NULL,
  last_sync_at INTEGER NOT NULL DEFAULT 0,
  last_error   TEXT NOT NULL DEFAULT '',
  -- Set by cut-over: the id of the cut-over sync and how many files it failed to import
  -- (0 and 0 before cut-over). The failures stay listed under that sync.
  cutover_sync_id INTEGER NOT NULL DEFAULT 0,
  cutover_failed  INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX nexus_sources_created_by ON nexus_sources(created_by);
CREATE INDEX nexus_sources_due ON nexus_sources(last_sync_at) WHERE state = 'mirroring';

-- The Nexus repos a source mirrors and the repo each one goes into. A target repo mirrors at
-- most one source; nexus_source_repos_target finds the source of a target. kind is the Nexus
-- repo type: a hosted repo is copied by the syncs; a proxy or a group becomes a proxy or a
-- group here when the repos are saved, and the syncs skip it.
CREATE TABLE nexus_source_repos (
  source_id      INTEGER NOT NULL REFERENCES nexus_sources(id) ON DELETE CASCADE,
  source_repo    TEXT NOT NULL,                      -- Nexus repo name
  source_format  TEXT NOT NULL,                      -- Nexus format id, e.g. "maven2"
  kind           TEXT NOT NULL DEFAULT 'hosted' CHECK (kind IN ('hosted','proxy','group')),
  target_repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  PRIMARY KEY (source_id, source_repo)
) STRICT, WITHOUT ROWID;
CREATE INDEX nexus_source_repos_target ON nexus_source_repos(target_repo_id);

-- One row per sync of a source. trigger is what started it: a save of the repos, an admin
-- ("Sync now"), the scheduler, or a cut-over (the final sync). The unique partial index allows
-- one running sync per source and finds the running syncs at startup.
CREATE TABLE nexus_syncs (
  id          INTEGER PRIMARY KEY,
  source_id   INTEGER NOT NULL REFERENCES nexus_sources(id) ON DELETE CASCADE,
  state       TEXT NOT NULL CHECK (state IN ('running','done','failed','cancelled')),
  trigger     TEXT NOT NULL CHECK (trigger IN ('save','manual','schedule','cutover')),
  started_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,  -- NULL: the scheduler
  started_at  INTEGER NOT NULL,
  finished_at INTEGER,
  error       TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX nexus_syncs_running ON nexus_syncs(source_id) WHERE state = 'running';
CREATE INDEX nexus_syncs_source ON nexus_syncs(source_id);
CREATE INDEX nexus_syncs_started_by ON nexus_syncs(started_by);

-- One row per mapped hosted repo of a sync, copied from nexus_source_repos when the sync starts.
-- continuation is the Nexus continuation token of the next page ('' = first page); the runner
-- saves it with the counters after each page. checked counts the files read from Nexus
-- (imported + skipped + ignored + failed), bytes the bytes imported, and active_at is when the
-- runner saved the last page (0: not yet).
CREATE TABLE nexus_sync_repos (
  sync_id        INTEGER NOT NULL REFERENCES nexus_syncs(id) ON DELETE CASCADE,
  source_repo    TEXT NOT NULL,
  source_format  TEXT NOT NULL,
  target_repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  continuation   TEXT NOT NULL DEFAULT '',
  checked        INTEGER NOT NULL DEFAULT 0,
  imported       INTEGER NOT NULL DEFAULT 0,
  skipped        INTEGER NOT NULL DEFAULT 0,
  ignored        INTEGER NOT NULL DEFAULT 0,
  failed         INTEGER NOT NULL DEFAULT 0,
  bytes          INTEGER NOT NULL DEFAULT 0,
  active_at      INTEGER NOT NULL DEFAULT 0,
  state          TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','running','done','failed','cancelled')),
  error          TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (sync_id, source_repo)
) STRICT, WITHOUT ROWID;
CREATE INDEX nexus_sync_repos_target ON nexus_sync_repos(target_repo_id);

-- One row per source file that a sync could not import. A resumed page records the same file
-- once.
CREATE TABLE nexus_sync_failures (
  id          INTEGER PRIMARY KEY,                   -- keyset cursor of the failure list
  sync_id     INTEGER NOT NULL REFERENCES nexus_syncs(id) ON DELETE CASCADE,
  source_repo TEXT NOT NULL,
  path        TEXT NOT NULL,
  reason      TEXT NOT NULL,
  UNIQUE (sync_id, source_repo, path)
) STRICT;
CREATE INDEX nexus_sync_failures_sync ON nexus_sync_failures(sync_id);

-- A short log per sync, for the admin: one line per event, oldest first by id. A line names a
-- repo and counts, never a path, a URL, a password or a token.
CREATE TABLE nexus_sync_log (
  id          INTEGER PRIMARY KEY,
  sync_id     INTEGER NOT NULL REFERENCES nexus_syncs(id) ON DELETE CASCADE,
  at          INTEGER NOT NULL,
  source_repo TEXT NOT NULL DEFAULT '',   -- '' for a line about the whole sync
  level       TEXT NOT NULL CHECK (level IN ('info','warn','error')),
  message     TEXT NOT NULL
) STRICT;
CREATE INDEX nexus_sync_log_sync ON nexus_sync_log(sync_id, id);
