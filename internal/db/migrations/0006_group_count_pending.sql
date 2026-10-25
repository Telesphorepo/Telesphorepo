-- 0006_group_count_pending.sql. db.Init runs this file once, in one transaction with foreign keys
-- off and checked before the commit. Every statement can run twice. Once a release has applied it,
-- never change it: add the next migration instead.

-- The rows of group_counts that the job core.group-counts is still counting. The triggers keep
-- such a row from its start, while the job counts the members' files in a read snapshot; the job
-- adds that count and deletes the row's entry here in one short write transaction. A reader never
-- reads a row that has an entry (GetGroupCount).
CREATE TABLE IF NOT EXISTS group_count_pending (
  count_id INTEGER PRIMARY KEY REFERENCES group_counts(id) ON DELETE CASCADE
) STRICT;
