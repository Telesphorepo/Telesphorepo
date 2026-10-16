-- Publish module schema: devices, device enrollments, CI trust policies, publish nonces and
-- publish sessions. References users, repos and builds of the kernel schema.

-- Enrolled device keys of the Telesphorepo CLI, one per computer and server. public_key is the
-- raw 32-byte Ed25519 key. A revoked or expired device cannot mint publish sessions.
CREATE TABLE devices (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
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

-- Nonces that a device signs. id_hash is SHA-256 of the nonce. A nonce with exchange = 0 is for
-- one request: a mint for one repo, a project setup or the revoke of the device. It lives 60 s,
-- and the request that takes it deletes it. A
-- nonce with exchange = 1 is for a format's token exchange: the device signs it over the repo
-- "*", and it mints for any repo until it expires (publish.session_ttl), one session per repo
-- (publish_exchange_sessions).
CREATE TABLE publish_nonces (
  id_hash    BLOB PRIMARY KEY,
  device_id  INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  exchange   INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX publish_nonces_device ON publish_nonces(device_id);
CREATE INDEX publish_nonces_expires ON publish_nonces(expires_at);

-- Publish sessions: the only credential that writes on a format route. The token is
-- "trs_<public_id>_<secret>"; secret_hash is SHA-256(secret). subject is who published: the
-- username (device, manual) or the CI token's sub (oidc). A session is open while closed_at is
-- NULL and expires_at is in the future. build_id is the build record of a CI session whose token
-- a CI provider read; it is NULL for every other session. Ended rows stay 30 days as the publish
-- log, keyset by id.
CREATE TABLE publish_sessions (
  id          INTEGER PRIMARY KEY,
  public_id   TEXT NOT NULL UNIQUE,
  secret_hash BLOB NOT NULL,
  repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  user_id     INTEGER REFERENCES users(id) ON DELETE SET NULL,
  device_id   INTEGER REFERENCES devices(id) ON DELETE SET NULL,
  trust_id    INTEGER REFERENCES publish_trusts(id) ON DELETE SET NULL,
  build_id    INTEGER REFERENCES builds(id) ON DELETE SET NULL,
  via         TEXT NOT NULL CHECK (via IN ('device','oidc','manual')),
  subject     TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,
  closed_at   INTEGER
) STRICT;
CREATE INDEX publish_sessions_expires ON publish_sessions(expires_at);
CREATE INDEX publish_sessions_repo ON publish_sessions(repo_id);
CREATE INDEX publish_sessions_user ON publish_sessions(user_id);
CREATE INDEX publish_sessions_device ON publish_sessions(device_id);
CREATE INDEX publish_sessions_trust ON publish_sessions(trust_id);
CREATE INDEX publish_sessions_build ON publish_sessions(build_id);

-- CI identity tokens already exchanged for a publish session, one row per repo. id_hash is
-- SHA-256 of "<issuer>\n<jti>", or of the whole token when it has no jti; the row lives until
-- the token's exp.
CREATE TABLE publish_oidc_seen (
  id_hash    BLOB NOT NULL,
  repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  session_id INTEGER REFERENCES publish_sessions(id) ON DELETE SET NULL, -- the session the token minted for the repo
  PRIMARY KEY (id_hash, repo_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX publish_oidc_seen_expires ON publish_oidc_seen(expires_at);
CREATE INDEX publish_oidc_seen_session ON publish_oidc_seen(session_id);
CREATE INDEX publish_oidc_seen_repo ON publish_oidc_seen(repo_id);

-- The publish session that a token exchange nonce minted for each repo. A token request with
-- the same nonce gets the session of the repo back while it is open, so a push keeps one
-- session (and its upload sessions) across token requests.
CREATE TABLE publish_exchange_sessions (
  nonce_hash BLOB NOT NULL REFERENCES publish_nonces(id_hash) ON DELETE CASCADE,
  repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  session_id INTEGER NOT NULL REFERENCES publish_sessions(id) ON DELETE CASCADE,
  PRIMARY KEY (nonce_hash, repo_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX publish_exchange_sessions_repo ON publish_exchange_sessions(repo_id);
CREATE INDEX publish_exchange_sessions_session ON publish_exchange_sessions(session_id);
