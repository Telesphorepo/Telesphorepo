-- Signing module schema: repository signing keys and the key of each repo. References repos of
-- the kernel schema.

-- private_key is the sealed armored private key without a passphrase (settings.Service.Encrypt,
-- AAD "signing_keys:<id>"), so the service inserts an empty value and sets it in the same
-- transaction. public_key is armored.
CREATE TABLE signing_keys (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  fingerprint TEXT NOT NULL,
  public_key  TEXT NOT NULL,
  private_key BLOB NOT NULL,
  created_at  INTEGER NOT NULL
) STRICT;

-- The key that signs each repo of a format that uses signing keys (the repo field signing_key).
-- A key in use cannot be deleted (ON DELETE RESTRICT); repo_signing_keys_key finds its repos.
CREATE TABLE repo_signing_keys (
  repo_id INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  key_id  INTEGER NOT NULL REFERENCES signing_keys(id) ON DELETE RESTRICT
) STRICT;
CREATE INDEX repo_signing_keys_key ON repo_signing_keys(key_id, repo_id);
