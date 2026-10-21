-- Edge module schema: the outbox of the R2 objects that the edge Worker reads, the hashes of the
-- access objects in the home bucket, and the state of the home bucket and of each region. It
-- references no table: an outbox row outlives a deleted repo, so its pointers are deleted too.

-- One row per dirty object. repo_id > 0: path is an asset path of that repo, whose pointer
-- p/<repo id>/<sha256 of path> the outbox writes or deletes. repo_id = 0: path is the key of an
-- access object (a/repo/<name>, a/user/<id>, a/token/<public id>, a/device/<id>) or config.json.
-- sha256 is the asset digest of the commit that wrote the row, '' to apply the current state. A
-- new row for a key replaces the old one and raises seq, so the worker deletes or delays only the
-- row that it read. due_at is unix milliseconds. timed_out_at (unix milliseconds, 0 for none) is
-- the last put whose outcome is unknown: the worker applies the state once more a minute later.
-- backfill is 1 for a row that the backfill of every pointer queued (kept when a later write
-- replaces the row): the home bucket is ready once no such row is left.
CREATE TABLE edge_outbox (
  repo_id      INTEGER NOT NULL,
  path         TEXT NOT NULL,
  sha256       TEXT NOT NULL DEFAULT '',
  due_at       INTEGER NOT NULL,
  attempts     INTEGER NOT NULL DEFAULT 0,
  seq          INTEGER NOT NULL DEFAULT 0,
  timed_out_at INTEGER NOT NULL DEFAULT 0,
  error        TEXT NOT NULL DEFAULT '',
  backfill     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (repo_id, path)
) STRICT, WITHOUT ROWID;
CREATE INDEX edge_outbox_due ON edge_outbox(due_at);
CREATE INDEX edge_outbox_backfill ON edge_outbox(repo_id) WHERE backfill = 1;

-- The access objects and config.json in the home bucket, by key under edge/v1/, with the hex
-- SHA-256 of the JSON written. The reconcile writes an object whose hash differs and deletes an
-- object whose row is gone.
CREATE TABLE edge_written (
  key        TEXT PRIMARY KEY,
  hash       TEXT NOT NULL,
  written_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- The state of edge delivery: "home" (the location the objects were written for, the backfill
-- of every pointer, deploy needed) and "region:<hint>" (bucket, filling or ready, the backfill
-- cursor) and "ttl" (the access_ttl of the last config.json written, and until when an earlier
-- config.json can still be held by a Worker). value is JSON.
CREATE TABLE edge_state (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT, WITHOUT ROWID;
