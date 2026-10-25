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
