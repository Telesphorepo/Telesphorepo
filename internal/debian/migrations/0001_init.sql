-- Debian plugin schema (spec §5.4, §9). Applied after the core track; references repos and assets.

CREATE TABLE deb_packages (
  id           INTEGER PRIMARY KEY,
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  distribution TEXT NOT NULL,
  component    TEXT NOT NULL,
  architecture TEXT NOT NULL,
  package      TEXT NOT NULL,
  version      TEXT NOT NULL,
  asset_id     INTEGER NOT NULL REFERENCES assets(id),
  filename     TEXT NOT NULL,                        -- pool path, e.g. pool/main/h/hello/hello_1.0_amd64.deb
  size         INTEGER NOT NULL,
  md5          TEXT NOT NULL,
  sha1         TEXT NOT NULL,
  sha256       TEXT NOT NULL,
  control      TEXT NOT NULL,                        -- control stanza without hash/Filename/Size fields
  created_at   INTEGER NOT NULL,
  UNIQUE (repo_id, distribution, component, architecture, package, version)
) STRICT;
-- The UNIQUE index above doubles as the repo_id foreign-key index and the streaming index for
-- Packages generation. asset_id needs its own index, or every asset write scans deb_packages.
CREATE INDEX deb_packages_asset ON deb_packages(asset_id);

CREATE TABLE deb_dirty (
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  distribution TEXT NOT NULL,
  component    TEXT NOT NULL,
  architecture TEXT NOT NULL,
  marked_at    INTEGER NOT NULL,                     -- unix nanoseconds, for the "re-dirtied during rebuild" check
  PRIMARY KEY (repo_id, distribution, component, architecture)
) STRICT, WITHOUT ROWID;

CREATE TABLE deb_index_files (
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  distribution TEXT NOT NULL,
  path         TEXT NOT NULL,                        -- relative to dists/{dist}/, e.g. main/binary-amd64/Packages.gz
  size         INTEGER NOT NULL,
  md5          TEXT NOT NULL,
  sha256       TEXT NOT NULL,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (repo_id, distribution, path)
) STRICT, WITHOUT ROWID;
