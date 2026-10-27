-- Arch Linux (pacman) module schema. Applied after the core schema; references repos and assets.
-- The kernel's rebuilds table holds the pending index rebuilds (grp: the architecture, or "sign").

-- One row per stored package (a file of pool/<name>/).
CREATE TABLE IF NOT EXISTS arch_packages (
  id         INTEGER PRIMARY KEY,
  repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  version    TEXT NOT NULL,                        -- [epoch:]pkgver-pkgrel
  arch       TEXT NOT NULL,                        -- an architecture of the repo, or any
  vkey       BLOB NOT NULL,                        -- arch_version_key(version): pacman's order (order.go)
  asset_id   INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
  filename   TEXT NOT NULL,                        -- pool/<name>/<name>-<version>-<arch>.pkg.tar.<ext>
  size       INTEGER NOT NULL,
  sha256     TEXT NOT NULL,
  sig_state  BLOB NOT NULL,                        -- the SHA-256 state after the file's bytes: its signatures
  desc       TEXT NOT NULL,                        -- the desc entry of the package in the indexes
  created_at INTEGER NOT NULL,
  UNIQUE (repo_id, name, version, arch)
) STRICT;
-- asset_id needs its own index, or every asset delete scans arch_packages.
CREATE INDEX IF NOT EXISTS arch_packages_asset ON arch_packages(asset_id);
-- The versions of a package, newest first (Versions, Builds).
CREATE INDEX IF NOT EXISTS arch_packages_order ON arch_packages(repo_id, name, vkey);
-- The newest package of a name in one architecture, and the packages of an architecture by name.
CREATE INDEX IF NOT EXISTS arch_packages_arch ON arch_packages(repo_id, arch, name, vkey);

-- The file list of a package: the files entry of the package in <repo>.files.
CREATE TABLE IF NOT EXISTS arch_package_files (
  package_id INTEGER PRIMARY KEY REFERENCES arch_packages(id) ON DELETE CASCADE,
  files      TEXT NOT NULL
) STRICT;

-- The entries of the index of each architecture: the package that a name installs there (the
-- newest of the architecture's own packages and the any packages).
CREATE TABLE IF NOT EXISTS arch_db_entries (
  repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  arch       TEXT NOT NULL,
  name       TEXT NOT NULL,
  package_id INTEGER NOT NULL REFERENCES arch_packages(id) ON DELETE CASCADE,
  PRIMARY KEY (repo_id, arch, name)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS arch_db_entries_package ON arch_db_entries(package_id);
