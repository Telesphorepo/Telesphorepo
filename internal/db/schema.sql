-- Kernel schema. Each module with tables owns its own schema.sql; a module schema may reference
-- these tables, and this schema references no module table. db.Init runs each schema once and
-- keeps its fingerprint in schema_components. Times are unix seconds, booleans INTEGER 0/1.
-- Every foreign-key child column has an index.

-- kind 'oidc' users have password_hash '!' (never verifies), no tokens, no Basic auth and no
-- second factor of their own.
CREATE TABLE users (
  id                  INTEGER PRIMARY KEY,
  username            TEXT NOT NULL UNIQUE,          -- ^[a-z0-9][a-z0-9._-]{0,63}$
  password_hash       TEXT NOT NULL,                 -- argon2id PHC string
  disabled            INTEGER NOT NULL DEFAULT 0,
  password_changed_at INTEGER NOT NULL,
  created_at          INTEGER NOT NULL,
  kind                TEXT NOT NULL DEFAULT 'local' CHECK (kind IN ('local','oidc')),
  oidc_issuer         TEXT,
  oidc_subject        TEXT,
  email               TEXT
) STRICT;
CREATE UNIQUE INDEX users_oidc ON users(oidc_issuer, oidc_subject) WHERE oidc_subject IS NOT NULL;

-- Server permissions: one row per person and permission ID (internal/perm). The permission
-- registry validates the IDs; a row whose ID no module registers is skipped when read. An
-- administrator holds every server permission. user_permissions_perm finds the holders of one.
CREATE TABLE user_permissions (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  perm    TEXT NOT NULL,
  PRIMARY KEY (user_id, perm)
) STRICT, WITHOUT ROWID;
CREATE INDEX user_permissions_perm ON user_permissions(perm, user_id);

-- Read tokens: they read repos and call the API, but never publish.
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
  id             INTEGER PRIMARY KEY AUTOINCREMENT,   -- never reused: edge objects are keyed by it
  name           TEXT NOT NULL UNIQUE,
  format         TEXT NOT NULL,
  kind           TEXT NOT NULL DEFAULT 'hosted' CHECK (kind IN ('hosted','proxy','group')),
  anonymous_read INTEGER NOT NULL DEFAULT 0,
  config         TEXT NOT NULL DEFAULT '{}',         -- JSON, format-specific
  created_at     INTEGER NOT NULL,
  description    TEXT NOT NULL DEFAULT '',
  -- Kept by the assets triggers below, in the transaction of each asset write: every asset row
  -- counts (artifacts, metadata, checksums and index files).
  file_count        INTEGER NOT NULL DEFAULT 0,
  total_size        INTEGER NOT NULL DEFAULT 0,
  last_published_at INTEGER NOT NULL DEFAULT 0       -- newest updated_at of a non-index asset; 0 for never
) STRICT;
CREATE INDEX repos_anonymous ON repos(id) WHERE anonymous_read = 1;

-- Repository permissions: one row per repo, person and permission ID (internal/perm). The
-- permission registry checks the IDs and their needs on every write.
CREATE TABLE repo_permissions (
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  perm    TEXT NOT NULL,
  PRIMARY KEY (repo_id, user_id, perm)
) STRICT, WITHOUT ROWID;
CREATE INDEX repo_permissions_user ON repo_permissions(user_id, perm);

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

-- Content-addressed blobs (internal/blobs). The storage key is blobs/sha256/<first 2 hex>/<hex>.
-- One blob backs any number of assets in any repos. refs counts the asset rows that point here
-- (the assets triggers keep it). unreferenced_at is created_at for a new row, the time refs
-- reached 0, and 0 while the blob is referenced; the blob GC deletes a row and its object once
-- refs is 0 for longer than blobs.gc_grace. blobs_unreferenced finds them.
CREATE TABLE blobs (
  id              INTEGER PRIMARY KEY,
  sha256          TEXT NOT NULL UNIQUE,              -- lowercase hex
  size            INTEGER NOT NULL,
  refs            INTEGER NOT NULL DEFAULT 0,
  unreferenced_at INTEGER NOT NULL,
  created_at      INTEGER NOT NULL,
  md5             TEXT NOT NULL DEFAULT '',           -- lowercase hex; '' for a synthetic row of the scale test
  sha1            TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX blobs_unreferenced ON blobs(unreferenced_at) WHERE refs = 0;

-- Build records (internal/ci): one row per CI run that published files, in a form that no CI
-- system shapes. provider is the ID of the CI provider that read the run from a verified CI
-- token; key is the provider's unique key of the run. links is JSON [{rel, label, url}] that only
-- the provider makes. A file points at the build that wrote it (assets.build_id); the publish
-- module deletes a build that no file references after 30 days. A session that names it keeps
-- its row with build_id NULL.
CREATE TABLE builds (
  id          INTEGER PRIMARY KEY,
  provider    TEXT NOT NULL,
  key         TEXT NOT NULL,
  repository  TEXT NOT NULL DEFAULT '',
  revision    TEXT NOT NULL DEFAULT '',
  ref         TEXT NOT NULL DEFAULT '',
  ref_name    TEXT NOT NULL DEFAULT '',
  ref_type    TEXT NOT NULL DEFAULT '',            -- "branch", "tag" or ""
  run         TEXT NOT NULL DEFAULT '',
  workflow    TEXT NOT NULL DEFAULT '',
  actor       TEXT NOT NULL DEFAULT '',
  links       TEXT NOT NULL DEFAULT '[]',
  created_at  INTEGER NOT NULL,
  UNIQUE (provider, key)
) STRICT;
CREATE INDEX builds_created ON builds(created_at);

-- id is the rowid that assets_fts points at, so assets keeps its rowid. size, sha256, md5 and sha1
-- are the blob's, copied (a blob never changes), so a download reads no other row. Only the asset pipeline (internal/assets) writes rows.
CREATE TABLE assets (
  id           INTEGER PRIMARY KEY,
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  path         TEXT NOT NULL,                        -- repo-relative, forward slashes
  blob_id      INTEGER NOT NULL REFERENCES blobs(id),
  size         INTEGER NOT NULL,
  sha256       TEXT NOT NULL,                        -- lowercase hex
  content_type TEXT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('artifact','metadata','index')),
  uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  build_id     INTEGER REFERENCES builds(id) ON DELETE SET NULL, -- the CI build that wrote the file
  -- The cached files of a proxy repo: when the remote last confirmed the file, and its ETag for
  -- the next check. A hosted file keeps the defaults.
  checked_at   INTEGER NOT NULL DEFAULT 0,
  remote_etag  TEXT NOT NULL DEFAULT '',
  md5          TEXT NOT NULL DEFAULT '',             -- lowercase hex, '' when the blob has none
  sha1         TEXT NOT NULL DEFAULT '',
  UNIQUE (repo_id, path)                             -- also the repo_id foreign-key index
) STRICT;
CREATE INDEX assets_uploaded_by ON assets(uploaded_by);
CREATE INDEX assets_build ON assets(build_id) WHERE build_id IS NOT NULL;
-- The assets of one blob in one repo (assets.Pipeline.ByBlob), and the blob_id foreign key.
CREATE INDEX assets_blob ON assets(blob_id, repo_id);

-- blobs.refs follows every asset insert, delete and change of blob_id in the same transaction.
CREATE TRIGGER assets_blob_ai AFTER INSERT ON assets BEGIN
  UPDATE blobs SET refs = refs + 1, unreferenced_at = 0 WHERE id = new.blob_id;
END;
CREATE TRIGGER assets_blob_ad AFTER DELETE ON assets BEGIN
  UPDATE blobs SET refs = refs - 1,
    unreferenced_at = CASE WHEN refs = 1 THEN CAST(strftime('%s', 'now') AS INTEGER) ELSE unreferenced_at END
  WHERE id = old.blob_id;
END;
CREATE TRIGGER assets_blob_au AFTER UPDATE OF blob_id ON assets WHEN new.blob_id <> old.blob_id BEGIN
  UPDATE blobs SET refs = refs + 1, unreferenced_at = 0 WHERE id = new.blob_id;
  UPDATE blobs SET refs = refs - 1,
    unreferenced_at = CASE WHEN refs = 1 THEN CAST(strftime('%s', 'now') AS INTEGER) ELSE unreferenced_at END
  WHERE id = old.blob_id;
END;

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

-- Repo operations (internal/ops): at most one per repo; an ended operation's row is deleted. An
-- active operation refuses writes, deletes and mints of the repo with 409. type is a registered
-- operation type ("delete", "mirror"), owner the module that began it, ref the owner's
-- reference (e.g. the Nexus source ID). repo_ops_running finds the running operations of a type
-- (the startup resume of repo deletes).
CREATE TABLE repo_ops (
  id         INTEGER PRIMARY KEY,
  repo_id    INTEGER NOT NULL UNIQUE REFERENCES repos(id) ON DELETE CASCADE,
  type       TEXT NOT NULL,
  owner      TEXT NOT NULL,
  ref        TEXT NOT NULL DEFAULT '',
  state      TEXT NOT NULL CHECK (state IN ('running','held','failed')),
  done       INTEGER NOT NULL DEFAULT 0,
  total      INTEGER NOT NULL DEFAULT 0,
  error      TEXT NOT NULL DEFAULT '',
  started_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
  started_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX repo_ops_started_by ON repo_ops(started_by);
CREATE INDEX repo_ops_running ON repo_ops(type) WHERE state = 'running';

-- Resumable uploads (assets.Uploads). The bytes are in $TR_DATA_DIR/tmp/uploads/<id>; received
-- is how many of them the file holds. publish_session_id is the publish session that started the
-- upload (no foreign key: the table belongs to the publish module); only it continues the upload.
-- An upload idle past upload.session_ttl expires: the core.upload-sessions job deletes the row
-- and the file. The job also deletes each file that has no row and was not written for
-- upload.session_ttl (its repo or user was deleted).
CREATE TABLE upload_sessions (
  id                 TEXT PRIMARY KEY,               -- 32 lowercase hex, random
  repo_id            INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  user_id            INTEGER REFERENCES users(id) ON DELETE CASCADE,
  publish_session_id INTEGER,
  received           INTEGER NOT NULL DEFAULT 0,
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL,
  expires_at         INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX upload_sessions_repo ON upload_sessions(repo_id);
CREATE INDEX upload_sessions_user ON upload_sessions(user_id);
CREATE INDEX upload_sessions_expires ON upload_sessions(expires_at);

-- The durable queue of format rebuilds (internal/rebuild): a unit of a group of a repo that a
-- format must build again, e.g. the Debian index of (distribution, "main/amd64"). grp is the unit
-- of debounce. marked_at is unix nanoseconds: a rebuild deletes only the rows marked before it
-- started.
CREATE TABLE rebuilds (
  repo_id   INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  grp       TEXT NOT NULL,
  unit      TEXT NOT NULL,
  marked_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, grp, unit)
) STRICT, WITHOUT ROWID;

-- In-app settings (internal/settings). value is JSON, or base64(nonce || AES-256-GCM ciphertext)
-- when the setting's definition is secret.
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  updated_by INTEGER REFERENCES users(id) ON DELETE SET NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX settings_updated_by ON settings(updated_by);

-- Second factors ---------------------------------------------------------------------------------

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

-- Passkeys (WebAuthn credentials). credential is the JSON of the library's webauthn.Credential:
-- the public key, the sign count, the flags and the transports. credential_id is its ID, the
-- lookup key of a sign-in.
CREATE TABLE user_passkeys (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id BLOB NOT NULL UNIQUE,
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

-- Short-lived single-use challenges: the second step of a sign-in, a pending TOTP setup, WebAuthn
-- ceremony data, the grant of an SSO device approval (kind 'device_grant', data the user
-- code) and the setup code of a project (kind 'setup_code', data the repo id as decimal text,
-- 10 minutes). id_hash is SHA-256 of the value the client holds; data is kind-specific (sealed when it
-- holds a secret). Taking a challenge deletes it; a new challenge deletes expired ones.
CREATE TABLE auth_challenges (
  id_hash    BLOB PRIMARY KEY,
  kind       TEXT NOT NULL,
  user_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
  data       BLOB NOT NULL DEFAULT X'',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX auth_challenges_expires ON auth_challenges(expires_at);
CREATE INDEX auth_challenges_user ON auth_challenges(user_id);

-- Audit log. actor is the username, or the CI token's subject for a CI publish
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
