-- npm module schema. Applied after the core schema; references repos. The rows describe the
-- packages of hosted repos: each version row belongs to its tarball asset, written in the commit
-- of the tarball and removed with it. Proxies and groups have no rows.

CREATE TABLE npm_packages (
  repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  name     TEXT NOT NULL,     -- the package name, e.g. "lib" or "@acme/lib"
  created  INTEGER NOT NULL,  -- unix seconds of the first version
  modified INTEGER NOT NULL,  -- unix seconds of the last change of a version or a tag
  rev      TEXT NOT NULL,     -- the _rev of the packument: "<n>-<hash of versions, deprecations and tags>"
  PRIMARY KEY (repo_id, name)
) STRICT, WITHOUT ROWID;

CREATE TABLE npm_versions (
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  version      TEXT NOT NULL,    -- strict SemVer
  sort_key     TEXT NOT NULL,    -- version.SemVer.SortKey: byte order is SemVer order
  tarball      TEXT NOT NULL,    -- the asset path of the tarball: <name>/-/<file>.tgz
  integrity    TEXT NOT NULL,    -- "sha512-<base64>"
  shasum       TEXT NOT NULL,    -- SHA-1 hex
  published_at INTEGER NOT NULL,
  deprecated   TEXT NOT NULL,    -- the deprecation message; "" for none
  -- Last, so a read of the columns above leaves the overflow pages of a large manifest unread.
  manifest     TEXT NOT NULL,    -- the version's package.json as published, without dist
  PRIMARY KEY (repo_id, name, version)
) STRICT, WITHOUT ROWID;
-- Versions of a package in SemVer order (packuments, the version browser).
CREATE INDEX npm_versions_sort ON npm_versions(repo_id, name, sort_key);
-- The version of a removed tarball.
CREATE UNIQUE INDEX npm_versions_tarball ON npm_versions(repo_id, tarball);

CREATE TABLE npm_tags (
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  name    TEXT NOT NULL,
  tag     TEXT NOT NULL,
  version TEXT NOT NULL,
  PRIMARY KEY (repo_id, name, tag)
) STRICT, WITHOUT ROWID;
