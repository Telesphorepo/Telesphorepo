-- 0003_trust_name_search.sql. db.Init runs this file once, in one transaction with foreign keys off
-- and checked before the commit. Every statement can run twice. Once a release has applied it,
-- never change it: add the next migration instead.

-- The grams of the trust policy names (internal/namesearch), for GET /api/publish/trusts?q=: one
-- row per distinct substring of 1 to 3 characters of the folded name (ASCII letters in lower case,
-- as lower() folds them). A search reads one gram's rows in (folded, name) order. The publish
-- service writes a policy's rows in the transaction of its create and update; the policy's row
-- takes them with it.
CREATE TABLE IF NOT EXISTS publish_trust_grams (
  gram     TEXT NOT NULL,
  folded   TEXT NOT NULL,
  name     TEXT NOT NULL,
  trust_id INTEGER NOT NULL REFERENCES publish_trusts(id) ON DELETE CASCADE,
  PRIMARY KEY (gram, folded, name)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS publish_trust_grams_trust ON publish_trust_grams(trust_id);

WITH RECURSIVE pos(id, name, i) AS (
  SELECT id, lower(name), 1 FROM publish_trusts
  UNION ALL
  SELECT id, name, i + 1 FROM pos WHERE i < length(name)
), len(n) AS (VALUES (1), (2), (3))
INSERT OR IGNORE INTO publish_trust_grams (gram, folded, name, trust_id)
SELECT substr(pos.name, pos.i, len.n), pos.name, t.name, t.id
FROM pos JOIN len ON pos.i + len.n - 1 <= length(pos.name) JOIN publish_trusts t ON t.id = pos.id;
