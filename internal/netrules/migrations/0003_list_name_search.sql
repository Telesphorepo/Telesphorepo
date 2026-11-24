-- 0003_list_name_search.sql. db.Init runs this file once, in one transaction with foreign keys off
-- and checked before the commit. Every statement can run twice. Once a release has applied it,
-- never change it: add the next migration instead.

-- The grams of the IP lists' names and titles (internal/namesearch), for GET /api/ip-lists?q=: one
-- row per distinct substring of 1 to 3 characters of the folded name or title (ASCII letters in
-- lower case, as lower() folds them). text is the folded name and title with a unit separator
-- between them (a title holds no control character but a newline), so a search keeps the lists
-- that hold its text from the rows of one gram alone. The network module writes a list's rows in
-- the transaction of its create and update, and a preset's at each start; the list's row takes
-- them with it.
CREATE TABLE IF NOT EXISTS ip_list_grams (
  gram    TEXT NOT NULL,
  list_id INTEGER NOT NULL REFERENCES ip_lists(id) ON DELETE CASCADE,
  text    TEXT NOT NULL,
  PRIMARY KEY (gram, list_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ip_list_grams_list ON ip_list_grams(list_id);

WITH RECURSIVE pos(id, value, i) AS (
  SELECT id, lower(name), 1 FROM ip_lists
  UNION ALL
  SELECT id, lower(title), 1 FROM ip_lists
  UNION ALL
  SELECT id, value, i + 1 FROM pos WHERE i < length(value)
), len(n) AS (VALUES (1), (2), (3))
INSERT OR IGNORE INTO ip_list_grams (gram, list_id, text)
SELECT substr(pos.value, pos.i, len.n), l.id, lower(l.name) || char(31) || lower(l.title)
FROM pos JOIN len ON pos.i + len.n - 1 <= length(pos.value) JOIN ip_lists l ON l.id = pos.id;
