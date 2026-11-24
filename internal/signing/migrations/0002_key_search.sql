-- 0002_key_search.sql. db.Init runs this file once, in one transaction with foreign keys off and
-- checked before the commit. Every statement can run twice. Once a release has applied it, never
-- change it: add the next migration instead.

-- The grams of the signing keys (internal/namesearch), for GET /api/signing-keys?q=: one row per
-- distinct substring of 1 to 3 characters of the folded name and per distinct 3-character
-- substring of the folded fingerprint (ASCII letters in lower case, as lower() folds them). name
-- and fingerprint are the folded name and fingerprint, so a search keeps the keys that hold its
-- text from the rows of one gram alone. A text matches a fingerprint only as a key ID (8 hex
-- digits or more), whose search reads a trigram, so a fingerprint has trigrams only and a text of
-- 1 or 2 characters reads no row of one. The signing module writes a key's rows in the
-- transaction of its create (a key is never renamed); the key's row takes them with it.
CREATE TABLE IF NOT EXISTS signing_key_grams (
  gram        TEXT NOT NULL,
  key_id      INTEGER NOT NULL REFERENCES signing_keys(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  PRIMARY KEY (gram, key_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS signing_key_grams_key ON signing_key_grams(key_id);

WITH RECURSIVE pos(id, value, fp, i) AS (
  SELECT id, lower(name), 0, 1 FROM signing_keys
  UNION ALL
  SELECT id, lower(fingerprint), 1, 1 FROM signing_keys
  UNION ALL
  SELECT id, value, fp, i + 1 FROM pos WHERE i < length(value)
), len(n) AS (VALUES (1), (2), (3))
INSERT OR IGNORE INTO signing_key_grams (gram, key_id, name, fingerprint)
SELECT substr(pos.value, pos.i, len.n), k.id, lower(k.name), lower(k.fingerprint)
FROM pos JOIN len ON pos.i + len.n - 1 <= length(pos.value) AND (pos.fp = 0 OR len.n = 3)
JOIN signing_keys k ON k.id = pos.id;
