-- RPM (dnf) module schema. Applied after the core schema; references repos and assets.
-- The kernel's rebuilds table holds the pending rebuilds (grp: "repodata", or "sign").

-- One row per stored package (a file of Packages/<name>/).
CREATE TABLE IF NOT EXISTS rpm_packages (
  id             INTEGER PRIMARY KEY,
  repo_id        INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  epoch          INTEGER NOT NULL,                  -- -1: the package has no epoch
  version        TEXT NOT NULL,
  release        TEXT NOT NULL,
  arch           TEXT NOT NULL,
  evr            TEXT NOT NULL,                     -- [epoch:]version-release, without an epoch of 0
  vkey           BLOB NOT NULL,                     -- rpm_version_key(epoch, version, release): rpm's order (order.go)
  asset_id       INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
  filename       TEXT NOT NULL,                     -- Packages/<name>/<name>-<version>-<release>.<arch>.rpm
  size           INTEGER NOT NULL,                  -- of the stored (signed) file
  sha256         TEXT NOT NULL,                     -- of the stored file: the pkgid of the index
  upload_sha256  TEXT NOT NULL,                     -- of the file as uploaded: an identical upload is kept
  format         INTEGER NOT NULL,                  -- RPMFORMAT: 4, or 6
  hdr_state      BLOB NOT NULL,                     -- the SHA-256 state after the header's bytes: its signatures
  header_start   INTEGER NOT NULL,                  -- where the header starts in the stored file
  header_len     INTEGER NOT NULL,
  build_time     INTEGER NOT NULL,
  installed_size INTEGER NOT NULL,
  archive_size   INTEGER NOT NULL,
  summary        TEXT NOT NULL,
  created_at     INTEGER NOT NULL,
  UNIQUE (repo_id, name, epoch, version, release, arch)
) STRICT;
-- asset_id needs its own index, or every asset delete scans rpm_packages.
CREATE INDEX IF NOT EXISTS rpm_packages_asset ON rpm_packages(asset_id);
-- The versions of a package, newest first (Versions, Builds), and the order of the index.
CREATE INDEX IF NOT EXISTS rpm_packages_order ON rpm_packages(repo_id, name, vkey);

-- The parts of a package's index entries that do not depend on the stored file.
CREATE TABLE IF NOT EXISTS rpm_package_parts (
  package_id INTEGER PRIMARY KEY REFERENCES rpm_packages(id) ON DELETE CASCADE,
  text       TEXT NOT NULL,                         -- <summary> to <url> of the primary entry
  format_a   TEXT NOT NULL,                         -- <rpm:license> to <rpm:sourcerpm>
  format_b   TEXT NOT NULL,                         -- the dependencies and the primary files
  files      TEXT NOT NULL,                         -- the filelists entry's files
  other      TEXT NOT NULL                          -- the other entry's changelog
) STRICT;

-- The repodata files that the current repomd.xml of a repo names, space-separated: a delete of
-- one of them builds the repodata again.
CREATE TABLE IF NOT EXISTS rpm_repodata (
  repo_id INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  hrefs   TEXT NOT NULL
) STRICT;
