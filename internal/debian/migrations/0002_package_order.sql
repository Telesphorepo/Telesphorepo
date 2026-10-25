-- The order of the versions and builds of a package (Browse), one row per deb_packages row: the
-- keys whose byte order is the order of the pages (order.go), so a page is a seek. folder is the
-- folder of the file, e.g. pool/main/h/hello/. deb_name_key and deb_build_key are SQL functions
-- of the module that compute the keys of a version.
CREATE TABLE IF NOT EXISTS deb_package_order (
  package_id INTEGER PRIMARY KEY REFERENCES deb_packages(id) ON DELETE CASCADE,
  repo_id    INTEGER NOT NULL,
  folder     TEXT NOT NULL,
  component  TEXT NOT NULL,
  package    TEXT NOT NULL,
  name_key   BLOB NOT NULL,  -- the version the row is a build of, in dpkg order
  build_key  BLOB NOT NULL   -- the build within that version: by stamp, then in dpkg order
) STRICT;
CREATE INDEX IF NOT EXISTS deb_package_order_page ON deb_package_order(repo_id, folder, component, package, name_key, build_key);
INSERT OR IGNORE INTO deb_package_order (package_id, repo_id, folder, component, package, name_key, build_key)
  SELECT id, repo_id, rtrim(filename, replace(filename, '/', '')), component, package, deb_name_key(version), deb_build_key(version)
  FROM deb_packages;
-- The pages read deb_package_order_page; no query reads deb_packages_package.
DROP INDEX IF EXISTS deb_packages_package;
