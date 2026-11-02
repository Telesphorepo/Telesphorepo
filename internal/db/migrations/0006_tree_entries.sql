-- 0006_tree_entries.sql. db.Init runs this file once, in one transaction with foreign keys off and
-- checked before the commit. Every statement can run twice. Once a release has applied it, never
-- change it: add the next migration instead.

-- The children of each folder of a repo (internal/assets), for GET /api/repos/{name}/tree: one row
-- per direct child, parent is "" or a folder ending in "/". kind 0 is a folder, kind 1 a file.
-- sort_key orders the children of a folder, so a page is one range of the primary key: a file's is
-- repopath.FileKey(name), its bytes ("x.jar" < "x.jar.sha1"); a folder's is
-- repopath.FolderKey(name), the versions first in version order ("1.2" < "1.10"), then the other
-- names by their bytes. A folder row counts the files
-- under it (files) and goes when the last one does; a file row holds the id of its asset. The
-- byte pipeline writes the rows in the transaction of each asset insert and delete; the repo row's
-- delete takes them with it.
CREATE TABLE IF NOT EXISTS tree_entries (
  repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  parent   TEXT NOT NULL,
  kind     INTEGER NOT NULL CHECK (kind IN (0, 1)),
  sort_key BLOB NOT NULL,
  name     TEXT NOT NULL,
  files    INTEGER NOT NULL DEFAULT 0,
  asset_id INTEGER,
  PRIMARY KEY (repo_id, parent, kind, sort_key)
) STRICT, WITHOUT ROWID;

-- The files: parent is the path up to its last "/" (rtrim drops every trailing character that is
-- not a "/").
INSERT OR IGNORE INTO tree_entries (repo_id, parent, kind, sort_key, name, files, asset_id)
SELECT repo_id, parent, 1, tree_file_key(substr(path, length(parent) + 1)), substr(path, length(parent) + 1), 0, id
FROM (SELECT repo_id, path, id, rtrim(path, replace(path, '/', '')) AS parent FROM assets);

-- The folders: every folder on the path of each file, with the count of the files under it.
WITH RECURSIVE walk(repo_id, parent, rest) AS (
  SELECT repo_id, '', path FROM assets
  UNION ALL
  SELECT repo_id, parent || substr(rest, 1, instr(rest, '/')), substr(rest, instr(rest, '/') + 1)
  FROM walk WHERE instr(rest, '/') > 0
)
INSERT OR IGNORE INTO tree_entries (repo_id, parent, kind, sort_key, name, files)
SELECT repo_id, parent, 0, tree_folder_key(name), name, count(*)
FROM (SELECT repo_id, parent, substr(rest, 1, instr(rest, '/') - 1) AS name FROM walk WHERE instr(rest, '/') > 0)
GROUP BY repo_id, parent, name;
