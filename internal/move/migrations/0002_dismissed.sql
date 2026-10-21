-- dismissed_at is set when an admin puts an ended move away: the Storage page then shows no move.
ALTER TABLE storage_moves ADD COLUMN dismissed_at INTEGER;