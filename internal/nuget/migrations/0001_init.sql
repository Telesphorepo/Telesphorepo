-- NuGet module schema. Applied after the core schema; references repos and assets. One row per
-- package version of a hosted repo, written in the transaction of the .nupkg commit and removed
-- with it. Proxies and groups have no rows.

CREATE TABLE nuget_versions (
  id           INTEGER PRIMARY KEY,
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  lower_id     TEXT NOT NULL,                         -- the package id lowered, as the flat container names it
  package_id   TEXT NOT NULL,                         -- the package id as the .nuspec writes it
  version      TEXT NOT NULL,                         -- the version with its metadata, normalised (NuGet ToFullString)
  normalized   TEXT NOT NULL,                         -- the identity: normalised, without metadata
  sort_key     TEXT NOT NULL,                         -- version.NuGet SortKey: byte order is NuGet version order
  semver2      INTEGER NOT NULL,                      -- 1: only a SemVer 2.0 client takes it
  prerelease   INTEGER NOT NULL,
  listed       INTEGER NOT NULL,
  meta         TEXT NOT NULL,                         -- JSON: the .nuspec fields of the registration and search
  nupkg_path   TEXT NOT NULL,                         -- v3-flatcontainer/<id>/<version>/<id>.<version>.nupkg
  asset_id     INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
  published_at INTEGER NOT NULL,
  UNIQUE (repo_id, lower_id, sort_key)
) STRICT;
-- The UNIQUE index above is the repo_id foreign-key index, the version order of a package and
-- the order of the package ids. asset_id needs its own index, or every asset delete scans the
-- table.
CREATE INDEX nuget_versions_asset ON nuget_versions(asset_id);
