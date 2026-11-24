-- 0003_sync_log_repo.sql. db.Init runs this file once, in one transaction with foreign keys off and
-- checked before the commit. Every statement can run twice. Once a release has applied it, never
-- change it: add the next migration instead.

-- The log lines of one repo of a sync, oldest first: GET /api/nexus/syncs/{id}/log?repo= reads a
-- keyset page of them as one range of this index, so its cost follows the page, not the log.
CREATE INDEX IF NOT EXISTS nexus_sync_log_repo ON nexus_sync_log(sync_id, source_repo, id);
