-- 0008_name_search.sql. db.Init runs this file once, in one transaction with foreign keys off and
-- checked before the commit. Every statement can run twice. Once a release has applied it, never
-- change it: add the next migration instead.

-- The grams of the repo names and the usernames (internal/namesearch): one row per distinct
-- substring of 1 to 3 characters of the name (a name is in lower case), so the names that hold a
-- text are one range of an index in name order. A repo's rows copy what a list filters on
-- (format, kind, public), so each list of GET /api/repos reads one index, as its list without a
-- text does (0005_performance.sql); the public indexes hold anonymous so that they cover the
-- list. repos.Service writes and deletes a repo's rows by the grams of its name, in the
-- transaction of each create, rename, update of public and delete (no index on the repo: about 50
-- rows of each of the six trees per repo). The auth service writes a user's rows in the
-- transaction of the create; the user's row takes them with it.
CREATE TABLE IF NOT EXISTS repo_name_grams (
  gram      TEXT NOT NULL,
  name      TEXT NOT NULL,
  format    TEXT NOT NULL,
  kind      TEXT NOT NULL,
  anonymous INTEGER NOT NULL,
  PRIMARY KEY (gram, name)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS repo_name_grams_format ON repo_name_grams(gram, format, name);
CREATE INDEX IF NOT EXISTS repo_name_grams_kind ON repo_name_grams(gram, kind, name);
CREATE INDEX IF NOT EXISTS repo_name_grams_anonymous ON repo_name_grams(gram, name, anonymous) WHERE anonymous = 1;
CREATE INDEX IF NOT EXISTS repo_name_grams_anonymous_format ON repo_name_grams(gram, format, name, anonymous) WHERE anonymous = 1;

CREATE TABLE IF NOT EXISTS user_name_grams (
  gram    TEXT NOT NULL,
  name    TEXT NOT NULL,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  PRIMARY KEY (gram, name)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS user_name_grams_user ON user_name_grams(user_id);

-- The rows of the repos and the users there are: each position of each name, with 1, 2 and 3
-- characters from it.
WITH RECURSIVE pos(id, name, i) AS (
  SELECT id, lower(name), 1 FROM repos
  UNION ALL
  SELECT id, name, i + 1 FROM pos WHERE i < length(name)
), len(n) AS (VALUES (1), (2), (3))
INSERT OR IGNORE INTO repo_name_grams (gram, name, format, kind, anonymous)
SELECT substr(pos.name, pos.i, len.n), r.name, r.format, r.kind, r.anonymous_read
FROM pos JOIN len ON pos.i + len.n - 1 <= length(pos.name) JOIN repos r ON r.id = pos.id;

WITH RECURSIVE pos(id, name, i) AS (
  SELECT id, lower(username), 1 FROM users
  UNION ALL
  SELECT id, name, i + 1 FROM pos WHERE i < length(name)
), len(n) AS (VALUES (1), (2), (3))
INSERT OR IGNORE INTO user_name_grams (gram, name, user_id)
SELECT substr(pos.name, pos.i, len.n), u.username, u.id
FROM pos JOIN len ON pos.i + len.n - 1 <= length(pos.name) JOIN users u ON u.id = pos.id;
