-- PyPI module schema. Applied after the core schema; references repos and assets. Only hosted
-- repos have rows: a proxy renders from its cached upstream pages, a group from its members.

-- The projects of a hosted repo: one row per normalised name that has at least one file.
CREATE TABLE pypi_projects (
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  project      TEXT NOT NULL,                        -- normalised name (PEP 503), e.g. my-lib
  display_name TEXT NOT NULL,                        -- the Name of the newest upload's metadata, e.g. My_Lib
  PRIMARY KEY (repo_id, project)
) STRICT, WITHOUT ROWID;

-- The distribution files (wheels and sdists) of a hosted repo.
CREATE TABLE pypi_files (
  id              INTEGER PRIMARY KEY,
  repo_id         INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  project         TEXT NOT NULL,
  version         TEXT NOT NULL,                     -- PEP 440 normal form
  sort_key        TEXT NOT NULL,                     -- version.PEP440.SortKey
  filename        TEXT NOT NULL,
  path            TEXT NOT NULL,                     -- packages/<project>/<filename>
  asset_id        INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
  sha256          TEXT NOT NULL,
  size            INTEGER NOT NULL,
  requires_python TEXT NOT NULL,                     -- "" for none
  metadata_sha256 TEXT NOT NULL,                     -- SHA-256 of the .metadata sibling; "" for none
  uploaded_at     INTEGER NOT NULL,
  UNIQUE (repo_id, filename)
) STRICT;
-- The project page and the version browser: the files of one project in version order.
CREATE INDEX pypi_files_project ON pypi_files(repo_id, project, sort_key);
-- A remove of an asset finds its file row.
CREATE INDEX pypi_files_asset ON pypi_files(asset_id);
