-- rev names the state of a crate's index file: it moves on with each publish, removal and yank
-- (max(the rev before + 1, unix ns of the change)), so the rendered file is kept per revision.
ALTER TABLE cargo_names ADD COLUMN rev INTEGER NOT NULL DEFAULT 0;