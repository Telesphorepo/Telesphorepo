-- Cargo module schema. Applied after the core schema; references repos and assets. Only hosted
-- repos have rows: a proxy serves the index files of its remote, a group those of its members.

-- One row per published version of a crate. The index file of a crate is rendered from its rows.
CREATE TABLE cargo_versions (
  id          INTEGER PRIMARY KEY,
  repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,                        -- the crate name as published, e.g. Serde_Json
  lower_name  TEXT NOT NULL,                        -- lowercased: the name of the index file
  version     TEXT NOT NULL,                        -- SemVer as published, e.g. 1.2.0+build.5
  sort_key    TEXT NOT NULL,                        -- version.SemVer.SortKey
  base_key    TEXT NOT NULL,                        -- the sort key without build metadata
  entry       TEXT NOT NULL,                        -- the index entry (JSON) with yanked false
  asset_id    INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE, -- the .crate file
  path        TEXT NOT NULL,                        -- crates/<name>/<name>-<version>.crate
  yanked      INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  -- A version is unique ignoring its build metadata, as on crates.io.
  UNIQUE (repo_id, lower_name, base_key)
) STRICT;
-- The index file and the version browser: every version of one crate in version order.
CREATE INDEX cargo_versions_sort ON cargo_versions(repo_id, lower_name, sort_key);
-- The rows of a removed .crate file. Without it every asset delete scans cargo_versions.
CREATE INDEX cargo_versions_asset ON cargo_versions(asset_id);

-- The crate names of a repo, folded (lowercase, "-" as "_"): two names that fold alike clash,
-- as on crates.io.
CREATE TABLE cargo_names (
  repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  folded_name TEXT NOT NULL,
  name        TEXT NOT NULL,                        -- the name that holds the folded name
  PRIMARY KEY (repo_id, folded_name)
) STRICT, WITHOUT ROWID;
