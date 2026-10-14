-- Core schema (spec §5.2, §5.5). Format plugins own their tables in their own migration track.
-- Times are unix seconds, booleans INTEGER 0/1. Every foreign-key child column has an index.

-- One row per migration track: "core" and each format plugin that has migrations.
CREATE TABLE schema_migrations (
  component TEXT PRIMARY KEY,
  version   INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- kind 'oidc' users have password_hash '!' (never verifies), no tokens and no Basic auth.
CREATE TABLE users (
  id                  INTEGER PRIMARY KEY,
  username            TEXT NOT NULL UNIQUE,          -- ^[a-z0-9][a-z0-9._-]{0,63}$
  password_hash       TEXT NOT NULL,                 -- argon2id PHC string
  is_admin            INTEGER NOT NULL DEFAULT 0,
  disabled            INTEGER NOT NULL DEFAULT 0,
  password_changed_at INTEGER NOT NULL,
  created_at          INTEGER NOT NULL,
  kind                TEXT NOT NULL DEFAULT 'local' CHECK (kind IN ('local','oidc')),
  oidc_issuer         TEXT,
  oidc_subject        TEXT,
  email               TEXT
) STRICT;
CREATE UNIQUE INDEX users_oidc ON users(oidc_issuer, oidc_subject) WHERE oidc_subject IS NOT NULL;

CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  public_id    TEXT NOT NULL UNIQUE,                 -- the 12-hex id embedded in the token
  secret_hash  BLOB NOT NULL,                        -- SHA-256(secret)
  name         TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER,                              -- NULL = never
  last_used_at INTEGER,                              -- updated at most once per minute
  kind         TEXT NOT NULL DEFAULT 'personal' CHECK (kind IN ('personal','minted')),
  UNIQUE (user_id, name)                             -- also the user_id foreign-key index
) STRICT;

CREATE TABLE sessions (
  id_hash    BLOB PRIMARY KEY,                       -- SHA-256(cookie value)
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX sessions_expires ON sessions(expires_at);
CREATE INDEX sessions_user ON sessions(user_id);

-- format is validated against the format registry, not by a CHECK.
CREATE TABLE repos (
  id             INTEGER PRIMARY KEY,
  name           TEXT NOT NULL UNIQUE,
  format         TEXT NOT NULL,
  anonymous_read INTEGER NOT NULL DEFAULT 0,
  config         TEXT NOT NULL DEFAULT '{}',         -- JSON, format-specific
  created_at     INTEGER NOT NULL,
  locked_by      TEXT                                -- set: uploads answer 409 with this text
) STRICT;
CREATE INDEX repos_anonymous ON repos(id) WHERE anonymous_read = 1;

CREATE TABLE repo_permissions (
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  level   TEXT NOT NULL CHECK (level IN ('read','write')),
  PRIMARY KEY (repo_id, user_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX repo_permissions_user ON repo_permissions(user_id);

-- id is the rowid that assets_fts points at, so assets keeps its rowid.
CREATE TABLE assets (
  id           INTEGER PRIMARY KEY,
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  path         TEXT NOT NULL,                        -- repo-relative, forward slashes
  size         INTEGER NOT NULL,
  sha256       TEXT NOT NULL,                        -- lowercase hex
  content_type TEXT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('artifact','metadata','index')),
  uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  UNIQUE (repo_id, path)                             -- also the repo_id foreign-key index
) STRICT;
CREATE INDEX assets_uploaded_by ON assets(uploaded_by);

-- Substring search over paths; generated index files are not indexed.
CREATE VIRTUAL TABLE assets_fts USING fts5(path, content='assets', content_rowid='id', tokenize='trigram');
CREATE TRIGGER assets_ai AFTER INSERT ON assets WHEN new.kind <> 'index' BEGIN
  INSERT INTO assets_fts(rowid, path) VALUES (new.id, new.path);
END;
CREATE TRIGGER assets_ad AFTER DELETE ON assets WHEN old.kind <> 'index' BEGIN
  INSERT INTO assets_fts(assets_fts, rowid, path) VALUES ('delete', old.id, old.path);
END;
-- path and kind never change on UPDATE (upserts only touch size/sha256/content_type/updated_at),
-- so no update trigger is needed.

-- In-app settings (spec §3). Secret values are base64(nonce || AES-256-GCM ciphertext).
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  secret     INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  updated_by INTEGER REFERENCES users(id) ON DELETE SET NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX settings_updated_by ON settings(updated_by);

-- Storage moves (spec §11). target is the sealed JSON of the target storage settings
-- (settings.Service.Encrypt, AAD "storage_moves:<id>"). The unique partial index allows one
-- copying move at a time and finds it with one index probe.
CREATE TABLE storage_moves (
  id          INTEGER PRIMARY KEY,
  target      BLOB NOT NULL,
  state       TEXT NOT NULL CHECK (state IN ('copying','done','failed','cancelled')),
  cursor      INTEGER NOT NULL DEFAULT 0,            -- id of the last asset the copier finished
  total       INTEGER NOT NULL,
  copied      INTEGER NOT NULL DEFAULT 0,
  skipped     INTEGER NOT NULL DEFAULT 0,
  failed      INTEGER NOT NULL DEFAULT 0,
  error       TEXT NOT NULL DEFAULT '',
  started_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  started_at  INTEGER NOT NULL,
  finished_at INTEGER
) STRICT;
CREATE UNIQUE INDEX storage_moves_copying ON storage_moves(state) WHERE state = 'copying';
CREATE INDEX storage_moves_started_by ON storage_moves(started_by);

-- One row per asset the copier could not copy. key is the storage key at the time, so the row
-- stays readable after the asset is deleted.
CREATE TABLE storage_move_failures (
  move_id  INTEGER NOT NULL REFERENCES storage_moves(id) ON DELETE CASCADE,
  asset_id INTEGER NOT NULL,
  key      TEXT NOT NULL,
  reason   TEXT NOT NULL,
  PRIMARY KEY (move_id, asset_id)
) STRICT, WITHOUT ROWID;

-- Nexus sources (spec §12). credentials is the sealed JSON {username, password}
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
-- most one source; nexus_source_repos_target finds the source of a target.
CREATE TABLE nexus_source_repos (
  source_id      INTEGER NOT NULL REFERENCES nexus_sources(id) ON DELETE CASCADE,
  source_repo    TEXT NOT NULL,                      -- Nexus repo name
  source_format  TEXT NOT NULL,                      -- Nexus format id, e.g. "maven2"
  target_repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  PRIMARY KEY (source_id, source_repo)
) STRICT, WITHOUT ROWID;
CREATE INDEX nexus_source_repos_target ON nexus_source_repos(target_repo_id);

-- One row per sync of a source. cutover marks the final sync of a cut-over. The unique partial
-- index allows one running sync per source and finds the running syncs at startup.
CREATE TABLE nexus_syncs (
  id          INTEGER PRIMARY KEY,
  source_id   INTEGER NOT NULL REFERENCES nexus_sources(id) ON DELETE CASCADE,
  state       TEXT NOT NULL CHECK (state IN ('running','done','failed','cancelled')),
  cutover     INTEGER NOT NULL DEFAULT 0,
  started_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,  -- NULL: the scheduler
  started_at  INTEGER NOT NULL,
  finished_at INTEGER,
  error       TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX nexus_syncs_running ON nexus_syncs(source_id) WHERE state = 'running';
CREATE INDEX nexus_syncs_source ON nexus_syncs(source_id);
CREATE INDEX nexus_syncs_started_by ON nexus_syncs(started_by);

-- One row per mapped repo of a sync, copied from nexus_source_repos when the sync starts.
-- continuation is the Nexus continuation token of the next page ('' = first page); the runner
-- saves it with the counters after each page.
CREATE TABLE nexus_sync_repos (
  sync_id        INTEGER NOT NULL REFERENCES nexus_syncs(id) ON DELETE CASCADE,
  source_repo    TEXT NOT NULL,
  source_format  TEXT NOT NULL,
  target_repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  continuation   TEXT NOT NULL DEFAULT '',
  imported       INTEGER NOT NULL DEFAULT 0,
  skipped        INTEGER NOT NULL DEFAULT 0,
  ignored        INTEGER NOT NULL DEFAULT 0,
  failed         INTEGER NOT NULL DEFAULT 0,
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