-- 0005_performance.sql. db.Init runs this file once, in one transaction with foreign keys off and
-- checked before the commit. Every statement can run twice. Once a release has applied it, never
-- change it: add the next migration instead.

-- The search index keys each path by repo_id << 40 | id, so the matches of one repo sit together
-- in rowid order: a page of a search is one seek past its cursor, and a repo that the reader
-- cannot read is one seek to the next repo. Contentless, with deletes (assets keeps the path);
-- generated index files are not indexed. An asset never changes its repo, path or kind.
DROP TRIGGER IF EXISTS assets_ai;
DROP TRIGGER IF EXISTS assets_ad;
DROP TABLE IF EXISTS assets_fts;

CREATE VIRTUAL TABLE IF NOT EXISTS assets_search USING fts5(path, tokenize='trigram', content='', contentless_delete=1);
INSERT OR REPLACE INTO assets_search(rowid, path) SELECT (repo_id << 40) | id, path FROM assets WHERE kind <> 'index';

CREATE TRIGGER IF NOT EXISTS assets_search_ai AFTER INSERT ON assets WHEN new.kind <> 'index' BEGIN
  INSERT INTO assets_search(rowid, path) VALUES ((new.repo_id << 40) | new.id, new.path);
END;
CREATE TRIGGER IF NOT EXISTS assets_search_ad AFTER DELETE ON assets WHEN old.kind <> 'index' BEGIN
  DELETE FROM assets_search WHERE rowid = (old.repo_id << 40) | old.id;
END;


-- The rows of group_counts that the job core.group-counts is still counting. The triggers keep
-- such a row from its start, while the job counts the members' files in a read snapshot; the job
-- adds that count and deletes the row's entry here in one short write transaction. A reader never
-- reads a row that has an entry (GetGroupCount).
CREATE TABLE IF NOT EXISTS group_count_pending (
  count_id INTEGER PRIMARY KEY REFERENCES group_counts(id) ON DELETE CASCADE
) STRICT;

-- The download window: the last 30 UTC days (today included) of download_stats, kept as totals so
-- GET /api/stats and the downloads of a repo are seeks. download_window_state holds the first day
-- of the window; the triggers of download_stats add each written row of the window to
-- download_window_days (a row per day) and download_window_repos (a row per repo), and subtract a
-- deleted row, so a repo delete or a retention delete leaves the totals. The totals change in the
-- transaction of the download_stats write. When a day leaves the window the server subtracts that
-- day's rows (internal/stats). download_window_repos_top serves the top repos.
CREATE TABLE IF NOT EXISTS download_window_state (
  id        INTEGER PRIMARY KEY CHECK (id = 1),
  first_day INTEGER NOT NULL
) STRICT;
INSERT OR IGNORE INTO download_window_state (id, first_day)
VALUES (1, (CAST(strftime('%s', 'now') AS INTEGER) / 86400 - 29) * 86400);

CREATE TABLE IF NOT EXISTS download_window_days (
  day       INTEGER PRIMARY KEY,
  downloads INTEGER NOT NULL DEFAULT 0,
  bytes     INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE TABLE IF NOT EXISTS download_window_repos (
  repo_id   INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  downloads INTEGER NOT NULL DEFAULT 0,
  bytes     INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX IF NOT EXISTS download_window_repos_top ON download_window_repos(downloads DESC, bytes DESC, repo_id);

INSERT OR IGNORE INTO download_window_days (day, downloads, bytes)
SELECT day, SUM(downloads), SUM(bytes) FROM download_stats
WHERE day >= (SELECT first_day FROM download_window_state) GROUP BY day;
INSERT OR IGNORE INTO download_window_repos (repo_id, downloads, bytes)
SELECT repo_id, SUM(downloads), SUM(bytes) FROM download_stats
WHERE day >= (SELECT first_day FROM download_window_state) GROUP BY repo_id;

DROP TRIGGER IF EXISTS download_stats_window_ai;
CREATE TRIGGER download_stats_window_ai AFTER INSERT ON download_stats
WHEN new.day >= (SELECT first_day FROM download_window_state) BEGIN
  INSERT INTO download_window_days (day, downloads, bytes) VALUES (new.day, new.downloads, new.bytes)
  ON CONFLICT (day) DO UPDATE SET downloads = download_window_days.downloads + excluded.downloads,
                                  bytes = download_window_days.bytes + excluded.bytes;
  INSERT INTO download_window_repos (repo_id, downloads, bytes) VALUES (new.repo_id, new.downloads, new.bytes)
  ON CONFLICT (repo_id) DO UPDATE SET downloads = download_window_repos.downloads + excluded.downloads,
                                      bytes = download_window_repos.bytes + excluded.bytes;
END;
DROP TRIGGER IF EXISTS download_stats_window_au;
CREATE TRIGGER download_stats_window_au AFTER UPDATE OF downloads, bytes ON download_stats
WHEN new.day >= (SELECT first_day FROM download_window_state) BEGIN
  UPDATE download_window_days SET downloads = downloads + new.downloads - old.downloads,
                                  bytes = bytes + new.bytes - old.bytes WHERE day = new.day;
  UPDATE download_window_repos SET downloads = downloads + new.downloads - old.downloads,
                                   bytes = bytes + new.bytes - old.bytes WHERE repo_id = new.repo_id;
END;
DROP TRIGGER IF EXISTS download_stats_window_ad;
CREATE TRIGGER download_stats_window_ad AFTER DELETE ON download_stats
WHEN old.day >= (SELECT first_day FROM download_window_state) BEGIN
  UPDATE download_window_days SET downloads = downloads - old.downloads, bytes = bytes - old.bytes WHERE day = old.day;
  UPDATE download_window_repos SET downloads = downloads - old.downloads, bytes = bytes - old.bytes WHERE repo_id = old.repo_id;
END;


-- The lists of repositories (GET /api/repos, internal/repos list.go) are keyset pages, each read
-- from one index: by name (the UNIQUE name), by format and name, by kind and name, the newest
-- publishes, and the largest repositories (GET /api/overview). The repositories that anyone reads
-- (anonymous_read) have partial indexes of their own, so a visitor's page is a seek too.
CREATE INDEX IF NOT EXISTS repos_format_name ON repos(format, name);
CREATE INDEX IF NOT EXISTS repos_kind_name ON repos(kind, name);
CREATE INDEX IF NOT EXISTS repos_published ON repos(last_published_at, id);
CREATE INDEX IF NOT EXISTS repos_size ON repos(total_size, id);
CREATE INDEX IF NOT EXISTS repos_anonymous_name ON repos(name) WHERE anonymous_read = 1;
CREATE INDEX IF NOT EXISTS repos_anonymous_format_name ON repos(format, name) WHERE anonymous_read = 1;
CREATE INDEX IF NOT EXISTS repos_anonymous_published ON repos(last_published_at, id) WHERE anonymous_read = 1;

-- The totals of every repo, one row (id 1): how many repos there are, their files and stored
-- bytes (the repo counters summed; a group's are 0), and how many public repos that are not
-- groups. The triggers below keep it in the transaction of every change of a repo row, the
-- counter updates of the assets triggers included. GET /api/overview reads it.
CREATE TABLE IF NOT EXISTS repo_totals (
  id      INTEGER PRIMARY KEY CHECK (id = 1),
  repos   INTEGER NOT NULL,
  files   INTEGER NOT NULL,
  bytes   INTEGER NOT NULL,
  public  INTEGER NOT NULL
) STRICT;
INSERT OR IGNORE INTO repo_totals (id, repos, files, bytes, public)
SELECT 1, count(*), coalesce(sum(file_count), 0), coalesce(sum(total_size), 0),
  coalesce(sum(anonymous_read = 1 AND kind <> 'group'), 0)
FROM repos;

CREATE TRIGGER IF NOT EXISTS repo_totals_ai AFTER INSERT ON repos BEGIN
  UPDATE repo_totals SET repos = repos + 1, files = files + new.file_count, bytes = bytes + new.total_size,
    public = public + (new.anonymous_read = 1 AND new.kind <> 'group')
  WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS repo_totals_ad AFTER DELETE ON repos BEGIN
  UPDATE repo_totals SET repos = repos - 1, files = files - old.file_count, bytes = bytes - old.total_size,
    public = public - (old.anonymous_read = 1 AND old.kind <> 'group')
  WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS repo_totals_au AFTER UPDATE OF file_count, total_size, anonymous_read ON repos BEGIN
  UPDATE repo_totals SET files = files + new.file_count - old.file_count, bytes = bytes + new.total_size - old.total_size,
    public = public + (new.anonymous_read = 1 AND new.kind <> 'group') - (old.anonymous_read = 1 AND old.kind <> 'group')
  WHERE id = 1;
END;
