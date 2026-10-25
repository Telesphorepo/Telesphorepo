-- 0006_repo_lists.sql. db.Init runs this file once, in one transaction with foreign keys off and
-- checked before the commit. Every statement can run twice. Once a release has applied it, never
-- change it: add the next migration instead.

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
