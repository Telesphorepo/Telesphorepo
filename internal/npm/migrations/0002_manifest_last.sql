-- manifest moves to the last column, so a read of the columns before it leaves the overflow pages
-- of a large manifest unread.
CREATE TABLE npm_versions_next (
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  version      TEXT NOT NULL,    -- strict SemVer
  sort_key     TEXT NOT NULL,    -- version.SemVer.SortKey: byte order is SemVer order
  tarball      TEXT NOT NULL,    -- the asset path of the tarball: <name>/-/<file>.tgz
  integrity    TEXT NOT NULL,    -- "sha512-<base64>"
  shasum       TEXT NOT NULL,    -- SHA-1 hex
  published_at INTEGER NOT NULL,
  deprecated   TEXT NOT NULL,    -- the deprecation message; "" for none
  manifest     TEXT NOT NULL,    -- the version's package.json as published, without dist
  PRIMARY KEY (repo_id, name, version)
) STRICT, WITHOUT ROWID;
INSERT INTO npm_versions_next (repo_id, name, version, sort_key, tarball, integrity, shasum, published_at, deprecated, manifest)
  SELECT repo_id, name, version, sort_key, tarball, integrity, shasum, published_at, deprecated, manifest FROM npm_versions;
DROP TABLE npm_versions;
ALTER TABLE npm_versions_next RENAME TO npm_versions;
-- Versions of a package in SemVer order (packuments, the version browser).
CREATE INDEX npm_versions_sort ON npm_versions(repo_id, name, sort_key);
-- The version of a removed tarball.
CREATE UNIQUE INDEX npm_versions_tarball ON npm_versions(repo_id, tarball);