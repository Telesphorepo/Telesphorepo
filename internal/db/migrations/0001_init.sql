-- Core schema (spec §5.2, §5.5). Format plugins own their tables in their own migration track.
-- Times are unix seconds, booleans INTEGER 0/1. Every foreign-key child column has an index.

-- One row per migration track: "core" and each format plugin that has migrations.
CREATE TABLE schema_migrations (
  component TEXT PRIMARY KEY,
  version   INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- kind 'oidc' users have password_hash '!' (never verifies), no tokens, no Basic auth and no
-- second factor of their own.
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
-- The enabled admins in username order: everyone who can download and publish every repo.
CREATE INDEX users_admins ON users(username) WHERE is_admin = 1 AND disabled = 0;

-- Read tokens (spec §7.1): they read repos and call the API, but never publish (§7.6).
CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  public_id    TEXT NOT NULL UNIQUE,                 -- the 12-hex id embedded in the token
  secret_hash  BLOB NOT NULL,                        -- SHA-256(secret)
  name         TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER,                              -- NULL = never
  last_used_at INTEGER,                              -- updated at most once per minute
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

-- format is validated against the format registry, not by a CHECK. kind: a hosted repo stores
-- what is published to it, a proxy repo keeps what it fetches from its remote (repo_proxies), a
-- group repo serves the files of its members (repo_group_members) and has none of its own.
CREATE TABLE repos (
  id             INTEGER PRIMARY KEY,
  name           TEXT NOT NULL UNIQUE,
  format         TEXT NOT NULL,
  kind           TEXT NOT NULL DEFAULT 'hosted' CHECK (kind IN ('hosted','proxy','group')),
  anonymous_read INTEGER NOT NULL DEFAULT 0,
  config         TEXT NOT NULL DEFAULT '{}',         -- JSON, format-specific
  created_at     INTEGER NOT NULL,
  locked_by      TEXT,                               -- set: uploads answer 409 with this text
  description    TEXT NOT NULL DEFAULT '',
  -- Kept by the assets triggers below, in the transaction of each asset write: every asset row
  -- counts (artifacts, metadata, checksums and index files).
  file_count        INTEGER NOT NULL DEFAULT 0,
  total_size        INTEGER NOT NULL DEFAULT 0,
  last_published_at INTEGER NOT NULL DEFAULT 0       -- newest updated_at of a non-index asset; 0 for never
) STRICT;
CREATE INDEX repos_anonymous ON repos(id) WHERE anonymous_read = 1;

CREATE TABLE repo_permissions (
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  level   TEXT NOT NULL CHECK (level IN ('read','write')),
  PRIMARY KEY (repo_id, user_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX repo_permissions_user ON repo_permissions(user_id);

-- The members of a group repo, in the order a read tries them. A member is a hosted or proxy
-- repo of the group's format; a repo can be in several groups. Request paths read the in-memory
-- copy of the repos service.
CREATE TABLE repo_group_members (
  group_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  member_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  position  INTEGER NOT NULL,
  PRIMARY KEY (group_id, member_id),
  UNIQUE (group_id, position)
) STRICT, WITHOUT ROWID;
CREATE INDEX repo_group_members_member ON repo_group_members(member_id);

-- The remote of a proxy repo. credentials is the sealed JSON {username, password}
-- (settings.Service.Encrypt, AAD "repo_proxy:<repo id>"); empty when the remote needs none.
-- Request paths read the in-memory copy of the repos service.
CREATE TABLE repo_proxies (
  repo_id        INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  remote_url     TEXT NOT NULL,                       -- http(s), ends with "/"
  credentials    BLOB NOT NULL DEFAULT x'',
  metadata_ttl   INTEGER NOT NULL DEFAULT 1800,       -- seconds a changing file is used before a check
  not_found_ttl  INTEGER NOT NULL DEFAULT 300,        -- seconds a remote 404 is remembered
  last_ok_at     INTEGER NOT NULL DEFAULT 0,          -- last successful remote request
  last_error     TEXT NOT NULL DEFAULT '',
  last_error_at  INTEGER NOT NULL DEFAULT 0
) STRICT;

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
  -- The CI publish session that wrote the file, set only when that session recorded build
  -- provenance (publish_sessions.ci_repository); the session row then stays as the build record.
  publish_session_id INTEGER REFERENCES publish_sessions(id) ON DELETE SET NULL,
  -- The cached files of a proxy repo: when the remote last confirmed the file, and its ETag for
  -- the next check. A hosted file keeps the defaults.
  checked_at   INTEGER NOT NULL DEFAULT 0,
  remote_etag  TEXT NOT NULL DEFAULT '',
  UNIQUE (repo_id, path)                             -- also the repo_id foreign-key index
) STRICT;
CREATE INDEX assets_uploaded_by ON assets(uploaded_by);
CREATE INDEX assets_publish_session ON assets(publish_session_id) WHERE publish_session_id IS NOT NULL;

-- Substring search over paths; generated index files are not indexed.
CREATE VIRTUAL TABLE assets_fts USING fts5(path, content='assets', content_rowid='id', tokenize='trigram');
CREATE TRIGGER assets_ai AFTER INSERT ON assets WHEN new.kind <> 'index' BEGIN
  INSERT INTO assets_fts(rowid, path) VALUES (new.id, new.path);
END;
CREATE TRIGGER assets_ad AFTER DELETE ON assets WHEN old.kind <> 'index' BEGIN
  INSERT INTO assets_fts(assets_fts, rowid, path) VALUES ('delete', old.id, old.path);
END;
-- path and kind never change on UPDATE (upserts only touch size/sha256/content_type/updated_at),
-- so no FTS update trigger is needed.

-- The repo counters (repos.file_count, total_size, last_published_at) follow every asset insert,
-- replace and delete in the same transaction. A delete never lowers last_published_at.
CREATE TRIGGER assets_count_ai AFTER INSERT ON assets BEGIN
  UPDATE repos SET file_count = file_count + 1, total_size = total_size + new.size,
    last_published_at = CASE WHEN new.kind <> 'index' THEN max(last_published_at, new.updated_at) ELSE last_published_at END
  WHERE id = new.repo_id;
END;
CREATE TRIGGER assets_count_au AFTER UPDATE OF size, updated_at ON assets BEGIN
  UPDATE repos SET total_size = total_size + new.size - old.size,
    last_published_at = CASE WHEN new.kind <> 'index' THEN max(last_published_at, new.updated_at) ELSE last_published_at END
  WHERE id = new.repo_id;
END;
CREATE TRIGGER assets_count_ad AFTER DELETE ON assets BEGIN
  UPDATE repos SET file_count = file_count - 1, total_size = total_size - old.size WHERE id = old.repo_id;
END;

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
-- Publisher credentials (spec §7.6) ------------------------------------------------------------

-- TOTP second factors. secret is sealed (settings.Service.Encrypt, AAD "user_totp:<id>"), so
-- the service inserts an empty secret and sets it in the same transaction. last_step is the
-- last accepted 30 s time step; a code of that step or an earlier one is refused.
CREATE TABLE user_totp (
  id         INTEGER PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  secret     BLOB NOT NULL,
  name       TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_step  INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX user_totp_user ON user_totp(user_id);

-- Passkeys (WebAuthn credentials). credential holds the JSON of the library's
-- webauthn.Credential (flags, transports, authenticator data) that a login check needs;
-- public_key and sign_count are its key fields as columns.
CREATE TABLE user_passkeys (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id BLOB NOT NULL UNIQUE,
  public_key    BLOB NOT NULL,
  sign_count    INTEGER NOT NULL DEFAULT 0,
  credential    BLOB NOT NULL,
  name          TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  last_used_at  INTEGER
) STRICT;
CREATE INDEX user_passkeys_user ON user_passkeys(user_id);

-- One-time recovery codes, argon2id PHC strings. A used code keeps its row with used_at set
-- until the user generates a new set.
CREATE TABLE user_recovery_codes (
  user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash TEXT NOT NULL,
  used_at   INTEGER,
  PRIMARY KEY (user_id, code_hash)
) STRICT, WITHOUT ROWID;

-- Enrolled device keys (Gradle plugin, Maven extension). public_key is the raw 32-byte Ed25519
-- key. A revoked or expired device cannot mint publish sessions.
CREATE TABLE devices (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('gradle','maven')),
  public_key   BLOB NOT NULL UNIQUE,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_used_at INTEGER,
  revoked_at   INTEGER
) STRICT;
CREATE INDEX devices_user ON devices(user_id);

-- Pending device enrollments (the RFC 8628 device flow). device_code_hash is SHA-256 of the
-- device code. Approval creates the device and sets approved_by and device_id; the poll that
-- returns the device_id deletes the row. polled_at is the time of the last poll (0: none).
CREATE TABLE device_enrollments (
  device_code_hash BLOB PRIMARY KEY,
  user_code        TEXT NOT NULL UNIQUE,             -- "ABCD-EFGH"
  name             TEXT NOT NULL,
  kind             TEXT NOT NULL CHECK (kind IN ('gradle','maven')),
  public_key       BLOB NOT NULL,
  created_at       INTEGER NOT NULL,
  expires_at       INTEGER NOT NULL,
  polled_at        INTEGER NOT NULL DEFAULT 0,
  approved_by      INTEGER REFERENCES users(id) ON DELETE CASCADE,
  device_id        INTEGER REFERENCES devices(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;
CREATE INDEX device_enrollments_expires ON device_enrollments(expires_at);
CREATE INDEX device_enrollments_approved_by ON device_enrollments(approved_by);
CREATE INDEX device_enrollments_device ON device_enrollments(device_id);

-- CI trust policies (OIDC). claims is a JSON object: claim name -> allowed values, each exact
-- or a "*" glob; every listed claim must match. jwks is a pasted JWKS ('' = fetch the issuer's
-- keys). publish_trusts_issuer finds the enabled policies of a token's issuer.
CREATE TABLE publish_trusts (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  issuer     TEXT NOT NULL,
  audience   TEXT NOT NULL,
  claims     TEXT NOT NULL DEFAULT '{}',
  jwks       TEXT NOT NULL DEFAULT '',
  created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  disabled   INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX publish_trusts_issuer ON publish_trusts(issuer) WHERE disabled = 0;
CREATE INDEX publish_trusts_created_by ON publish_trusts(created_by);

-- The repos a trust policy may publish to.
CREATE TABLE publish_trust_repos (
  trust_id INTEGER NOT NULL REFERENCES publish_trusts(id) ON DELETE CASCADE,
  repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  PRIMARY KEY (trust_id, repo_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX publish_trust_repos_repo ON publish_trust_repos(repo_id);

-- Publish sessions: the only credential that writes on a format route. The token is
-- "trs_<public_id>_<secret>"; secret_hash is SHA-256(secret). subject is who published: the
-- username (device, manual) or the CI token's sub (oidc). A session is open while closed_at is
-- NULL and expires_at is in the future. Ended rows stay 30 days as the publish log, keyset by id.
-- The ci_* columns are the build provenance of a session minted from a CI token with GitHub
-- Actions claims ('' for every other session): ci_server is the web URL of the GitHub server
-- (https://github.com, or the issuer's origin for GitHub Enterprise Server and compatible
-- servers). A provenance session that wrote files is kept (kept = 1) after the 30 days as the
-- record of that build; publish_sessions_ci_ended finds the ended ones not settled yet.
CREATE TABLE publish_sessions (
  id          INTEGER PRIMARY KEY,
  public_id   TEXT NOT NULL UNIQUE,
  secret_hash BLOB NOT NULL,
  repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  user_id     INTEGER REFERENCES users(id) ON DELETE SET NULL,
  device_id   INTEGER REFERENCES devices(id) ON DELETE SET NULL,
  trust_id    INTEGER REFERENCES publish_trusts(id) ON DELETE SET NULL,
  via         TEXT NOT NULL CHECK (via IN ('device','oidc','manual')),
  subject     TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,
  closed_at   INTEGER,
  ci_server       TEXT NOT NULL DEFAULT '',
  ci_repository   TEXT NOT NULL DEFAULT '',          -- "owner/name"; '' = no provenance
  ci_sha          TEXT NOT NULL DEFAULT '',
  ci_ref          TEXT NOT NULL DEFAULT '',
  ci_run_id       TEXT NOT NULL DEFAULT '',
  ci_run_attempt  TEXT NOT NULL DEFAULT '',
  ci_workflow     TEXT NOT NULL DEFAULT '',          -- the workflow's name
  ci_workflow_ref TEXT NOT NULL DEFAULT '',          -- "owner/name/.github/workflows/x.yml@ref"
  ci_actor        TEXT NOT NULL DEFAULT '',
  kept            INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX publish_sessions_expires ON publish_sessions(expires_at) WHERE ci_repository = '';
CREATE INDEX publish_sessions_ci_ended ON publish_sessions(expires_at) WHERE ci_repository <> '' AND kept = 0;
CREATE INDEX publish_sessions_repo ON publish_sessions(repo_id);
CREATE INDEX publish_sessions_user ON publish_sessions(user_id);
CREATE INDEX publish_sessions_device ON publish_sessions(device_id);
CREATE INDEX publish_sessions_trust ON publish_sessions(trust_id);

-- Short-lived single-use challenges: the publish nonce (kind 'publish_nonce', device_id set),
-- WebAuthn ceremony data, a pending TOTP setup and the second step of a sign-in. id_hash is
-- SHA-256 of the value the client holds; data is kind-specific (sealed when it holds a secret).
-- Taking a challenge deletes it.
CREATE TABLE auth_challenges (
  id_hash    BLOB PRIMARY KEY,
  kind       TEXT NOT NULL,
  user_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
  device_id  INTEGER REFERENCES devices(id) ON DELETE CASCADE,
  data       BLOB NOT NULL DEFAULT X'',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX auth_challenges_expires ON auth_challenges(expires_at);
CREATE INDEX auth_challenges_user ON auth_challenges(user_id);
CREATE INDEX auth_challenges_device ON auth_challenges(device_id);

-- CI identity tokens already exchanged for a publish session. id_hash is SHA-256 of
-- "<issuer>\n<jti>", or of the whole token when it has no jti; the row lives until the
-- token's exp.
CREATE TABLE publish_oidc_seen (
  id_hash    BLOB PRIMARY KEY,
  expires_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX publish_oidc_seen_expires ON publish_oidc_seen(expires_at);

-- Audit log (spec §13). actor is the username, or the CI token's subject for a CI publish
-- session, or the attempted username of a failed sign-in. detail is a JSON object (never a
-- secret value). ip is the client address; this table is the only place that shows it. The
-- admin list is keyset by id, newest first; audit_events_at serves the retention cleanup.
CREATE TABLE audit_events (
  id            INTEGER PRIMARY KEY,
  at            INTEGER NOT NULL,
  actor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  actor         TEXT NOT NULL,
  action        TEXT NOT NULL,
  repo_id       INTEGER REFERENCES repos(id) ON DELETE SET NULL,
  target        TEXT NOT NULL DEFAULT '',
  detail        TEXT NOT NULL DEFAULT '{}',
  ip            TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX audit_events_at ON audit_events(at);
CREATE INDEX audit_events_repo ON audit_events(repo_id, id);
CREATE INDEX audit_events_action ON audit_events(action, id);
CREATE INDEX audit_events_actor ON audit_events(actor_user_id);

-- Named IP lists (internal/netrules). A 'preset' list (cloudflare, github-actions) is fetched
-- from source and refreshed daily: ranges is the last list that fetched and parsed, fetched_at
-- the unix time of that fetch (0: never) and error the last refresh error ('' after a good
-- refresh). A 'custom' list holds the ranges an admin entered. ranges is a JSON array of CIDRs.
-- Request paths read the in-memory snapshot, never this table.
CREATE TABLE ip_lists (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  kind        TEXT NOT NULL CHECK (kind IN ('preset', 'custom')),
  title       TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  ranges      TEXT NOT NULL DEFAULT '[]',
  source      TEXT NOT NULL DEFAULT '',
  fetched_at  INTEGER NOT NULL DEFAULT 0,
  error       TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
) STRICT;

-- Per-repo IP allow-lists (internal/netrules). A scope without rows is unrestricted. kind
-- 'cidr' holds a CIDR, kind 'list' the name of an IP list. Request paths read the in-memory
-- copy. repo_ip_rules_list finds the rules that name a list.
CREATE TABLE repo_ip_rules (
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  scope   TEXT NOT NULL CHECK (scope IN ('read', 'publish')),
  kind    TEXT NOT NULL CHECK (kind IN ('cidr', 'list')),
  value   TEXT NOT NULL,
  note    TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (repo_id, scope, kind, value)
) STRICT, WITHOUT ROWID;
CREATE INDEX repo_ip_rules_list ON repo_ip_rules(value, repo_id) WHERE kind = 'list';

-- Repository signing keys (internal/signing). private_key is the sealed armored private key
-- without a passphrase (settings.Service.Encrypt, AAD "signing_keys:<id>"), so the service
-- inserts an empty value and sets it in the same transaction. public_key is armored. A repo
-- names its key in its config ("signing_key"); repos_signing_key finds the repos that use a
-- key, and the triggers refuse a repo config that names a key that does not exist.
CREATE TABLE signing_keys (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  fingerprint TEXT NOT NULL,
  public_key  TEXT NOT NULL,
  private_key BLOB NOT NULL,
  created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX repos_signing_key ON repos(json_extract(config, '$.signing_key'));
CREATE TRIGGER repos_signing_key_insert BEFORE INSERT ON repos
WHEN json_extract(new.config, '$.signing_key') IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM signing_keys WHERE name = json_extract(new.config, '$.signing_key'))
BEGIN
  SELECT RAISE(ABORT, 'the signing key of the repo config does not exist');
END;
CREATE TRIGGER repos_signing_key_update BEFORE UPDATE OF config ON repos
WHEN json_extract(new.config, '$.signing_key') IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM signing_keys WHERE name = json_extract(new.config, '$.signing_key'))
BEGIN
  SELECT RAISE(ABORT, 'the signing key of the repo config does not exist');
END;