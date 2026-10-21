-- Storage module schema: storage moves. References users of the kernel schema.

-- One row per storage move. target is the sealed JSON of the target storage settings
-- (settings.Service.Encrypt, AAD "storage_moves:<id>"). dismissed_at is set when an admin puts an
-- ended move away: the Storage page then shows no move. The unique partial index allows one
-- copying move at a time and finds it with one index probe.
CREATE TABLE storage_moves (
  id          INTEGER PRIMARY KEY,
  target      BLOB NOT NULL,
  state       TEXT NOT NULL CHECK (state IN ('copying','done','failed','cancelled')),
  cursor      INTEGER NOT NULL DEFAULT 0,            -- id of the last blob the copier finished
  total       INTEGER NOT NULL,
  copied      INTEGER NOT NULL DEFAULT 0,
  skipped     INTEGER NOT NULL DEFAULT 0,
  failed      INTEGER NOT NULL DEFAULT 0,
  error       TEXT NOT NULL DEFAULT '',
  started_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  started_at  INTEGER NOT NULL,
  finished_at INTEGER,
  dismissed_at INTEGER
) STRICT;
CREATE UNIQUE INDEX storage_moves_copying ON storage_moves(state) WHERE state = 'copying';
CREATE INDEX storage_moves_started_by ON storage_moves(started_by);

-- One row per blob the copier could not copy. key is the storage key, so the row stays readable
-- after the blob is deleted.
CREATE TABLE storage_move_failures (
  move_id  INTEGER NOT NULL REFERENCES storage_moves(id) ON DELETE CASCADE,
  blob_id  INTEGER NOT NULL,
  key      TEXT NOT NULL,
  reason   TEXT NOT NULL,
  PRIMARY KEY (move_id, blob_id)
) STRICT, WITHOUT ROWID;
