-- One row per package of a hosted repo that has a version. rev names the state of the package's
-- documents (version list, registration): it moves on with each push, removal, unlist and
-- relist, so the rendered documents are kept per revision. The search finds package ids here.
CREATE TABLE nuget_packages (
  id       INTEGER PRIMARY KEY,                     -- the rowid that nuget_packages_fts points at
  repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  lower_id TEXT NOT NULL,                           -- never changes, so the FTS needs no update trigger
  rev      INTEGER NOT NULL,                        -- max(the rev before + 1, unix ns of the change)
  UNIQUE (repo_id, lower_id)
) STRICT;

-- Substring search over the package ids.
CREATE VIRTUAL TABLE nuget_packages_fts USING fts5(lower_id, content='nuget_packages', content_rowid='id', tokenize='trigram');
CREATE TRIGGER nuget_packages_ai AFTER INSERT ON nuget_packages BEGIN
  INSERT INTO nuget_packages_fts(rowid, lower_id) VALUES (new.id, new.lower_id);
END;
CREATE TRIGGER nuget_packages_ad AFTER DELETE ON nuget_packages BEGIN
  INSERT INTO nuget_packages_fts(nuget_packages_fts, rowid, lower_id) VALUES ('delete', old.id, old.lower_id);
END;

-- The packages that have versions already.
INSERT INTO nuget_packages (repo_id, lower_id, rev)
  SELECT DISTINCT repo_id, lower_id, 1 FROM nuget_versions;