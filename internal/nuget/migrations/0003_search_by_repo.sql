-- The search index of the package ids keys each package by repo_id << 40 | id, so the packages
-- of one repo sit together in rowid order: the search of a repo is a range of rowids, and a page
-- is one seek. Contentless, with deletes (nuget_packages keeps lower_id, which never changes).
DROP TRIGGER IF EXISTS nuget_packages_ai;
DROP TRIGGER IF EXISTS nuget_packages_ad;
DROP TABLE IF EXISTS nuget_packages_fts;

CREATE VIRTUAL TABLE IF NOT EXISTS nuget_packages_search USING fts5(lower_id, tokenize='trigram', content='', contentless_delete=1);
INSERT OR REPLACE INTO nuget_packages_search(rowid, lower_id) SELECT (repo_id << 40) | id, lower_id FROM nuget_packages;

CREATE TRIGGER IF NOT EXISTS nuget_packages_search_ai AFTER INSERT ON nuget_packages BEGIN
  INSERT INTO nuget_packages_search(rowid, lower_id) VALUES ((new.repo_id << 40) | new.id, new.lower_id);
END;
CREATE TRIGGER IF NOT EXISTS nuget_packages_search_ad AFTER DELETE ON nuget_packages BEGIN
  DELETE FROM nuget_packages_search WHERE rowid = (old.repo_id << 40) | old.id;
END;

-- The listed releases of each package: the versions of a search answer without prereleases, and
-- the newest release of a package, are a seek however many prereleases the package has.
CREATE INDEX IF NOT EXISTS nuget_versions_releases ON nuget_versions(repo_id, lower_id, sort_key) WHERE listed = 1 AND prerelease = 0;

-- The newest listed version of each package per search variant (its sort_key; NULL when the
-- package has none): release without SemVer 2.0 versions, release2 with them, pre and pre2 the
-- same with the prereleases. Written with each move of the package's rev, so a search reads
-- whether a package shows, and its version, in one seek.
CREATE TABLE IF NOT EXISTS nuget_package_latest (
  package  INTEGER PRIMARY KEY REFERENCES nuget_packages(id) ON DELETE CASCADE,
  release  TEXT,
  release2 TEXT,
  pre      TEXT,
  pre2     TEXT
) STRICT;

INSERT OR REPLACE INTO nuget_package_latest (package, release, release2, pre, pre2)
SELECT p.id,
  (SELECT v.sort_key FROM nuget_versions v WHERE v.repo_id = p.repo_id AND v.lower_id = p.lower_id
    AND v.listed = 1 AND v.prerelease = 0 AND v.semver2 = 0 ORDER BY v.sort_key DESC LIMIT 1),
  (SELECT v.sort_key FROM nuget_versions v WHERE v.repo_id = p.repo_id AND v.lower_id = p.lower_id
    AND v.listed = 1 AND v.prerelease = 0 ORDER BY v.sort_key DESC LIMIT 1),
  (SELECT v.sort_key FROM nuget_versions v WHERE v.repo_id = p.repo_id AND v.lower_id = p.lower_id
    AND v.listed = 1 AND v.semver2 = 0 ORDER BY v.sort_key DESC LIMIT 1),
  (SELECT v.sort_key FROM nuget_versions v WHERE v.repo_id = p.repo_id AND v.lower_id = p.lower_id
    AND v.listed = 1 ORDER BY v.sort_key DESC LIMIT 1)
FROM nuget_packages p;