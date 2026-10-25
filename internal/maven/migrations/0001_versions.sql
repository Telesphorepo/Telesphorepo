-- Maven module schema. Applied after the core schema; references repos. The hooks of the format
-- write the rows in the transaction of the asset change, for hosted and proxy repos (a proxy
-- lists the versions it has cached), so a page of versions or builds is a keyset seek.

-- One row per build of a version folder: the files <artifactId>-<version>[-<classifier>].<ext>
-- of a release, or one timestamp of a -SNAPSHOT (and its file without a timestamp). The row goes
-- with the last file of the build.
CREATE TABLE IF NOT EXISTS maven_builds (
  repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  folder   TEXT NOT NULL,     -- the version folder, com/acme/lib/1.0-SNAPSHOT/
  sort_key BLOB NOT NULL,     -- buildKey: byte order is build order, oldest first
  build    TEXT NOT NULL,     -- the build id: the version, a timestamp-number or SNAPSHOT
  PRIMARY KEY (repo_id, folder, sort_key)
) STRICT, WITHOUT ROWID;

-- One row per version folder that holds a build. The row goes with its last build.
CREATE TABLE IF NOT EXISTS maven_versions (
  repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  folder   TEXT NOT NULL,     -- the artifact folder, com/acme/lib/
  sort_key BLOB NOT NULL,     -- versionKey: byte order is version order, oldest first
  version  TEXT NOT NULL,     -- the name of the version folder
  snapshot INTEGER NOT NULL,  -- 1 for a -SNAPSHOT, 0 for a release
  builds   INTEGER NOT NULL,  -- its rows in maven_builds
  PRIMARY KEY (repo_id, folder, sort_key)
) STRICT, WITHOUT ROWID;
-- The newest release of an artifact, else its newest snapshot.
CREATE INDEX IF NOT EXISTS maven_versions_latest ON maven_versions(repo_id, folder, snapshot, sort_key);

-- While it has its row, the module's Init indexes the files that the repos hold already, then
-- deletes the row.
CREATE TABLE IF NOT EXISTS maven_index_pending (
  one INTEGER PRIMARY KEY CHECK (one = 1)
) STRICT;
INSERT OR IGNORE INTO maven_index_pending (one) VALUES (1);
