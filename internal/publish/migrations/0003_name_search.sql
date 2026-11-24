-- 0003_name_search.sql. db.Init runs this file once, in one transaction with foreign keys off
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

-- The grams of the devices (internal/namesearch), for GET /api/devices?q=: one row per distinct
-- substring of 1 to 3 characters of the folded name of the device or of the username of its owner
-- (ASCII letters in lower case, as lower() folds them). text is the folded username and device
-- name with a unit separator between them (neither holds a control character), so a search keeps
-- the devices that hold its text from the rows of one gram alone. A page is newest first, as the
-- list: the rows of one gram in device order, every device's or one person's (device_grams_user,
-- which holds text so that it covers the search). The publish service writes a device's rows in
-- the transaction of its create (neither a device nor a username is renamed); the device's row
-- takes them with it.
CREATE TABLE IF NOT EXISTS device_grams (
  gram      TEXT NOT NULL,
  device_id INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  user_id   INTEGER NOT NULL,
  text      TEXT NOT NULL,
  PRIMARY KEY (gram, device_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS device_grams_user ON device_grams(gram, user_id, device_id, text);
CREATE INDEX IF NOT EXISTS device_grams_device ON device_grams(device_id);

WITH RECURSIVE pos(id, value, i) AS (
  SELECT d.id, lower(u.username), 1 FROM devices d JOIN users u ON u.id = d.user_id
  UNION ALL
  SELECT id, lower(name), 1 FROM devices
  UNION ALL
  SELECT id, value, i + 1 FROM pos WHERE i < length(value)
), len(n) AS (VALUES (1), (2), (3))
INSERT OR IGNORE INTO device_grams (gram, device_id, user_id, text)
SELECT substr(pos.value, pos.i, len.n), d.id, d.user_id, lower(u.username) || char(31) || lower(d.name)
FROM pos JOIN len ON pos.i + len.n - 1 <= length(pos.value)
JOIN devices d ON d.id = pos.id JOIN users u ON u.id = d.user_id;

-- The publish log of one person in one repo (GET /api/publish/log?user=&repo=), newest first: a
-- range of this index in id order. A person's alone reads publish_sessions_user, a repo's alone
-- publish_sessions_repo (an index holds the id after its columns).
CREATE INDEX IF NOT EXISTS publish_sessions_user_repo ON publish_sessions(user_id, repo_id);
