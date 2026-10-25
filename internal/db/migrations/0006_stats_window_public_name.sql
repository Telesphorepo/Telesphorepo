-- 0006_stats_window_public_name.sql. db.Init runs this file once. Every statement can run twice.

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

-- A user's access (internal/access) reads the public repos of a page in name order through this
-- index.
CREATE INDEX IF NOT EXISTS repos_anonymous_name ON repos(name) WHERE anonymous_read = 1;
