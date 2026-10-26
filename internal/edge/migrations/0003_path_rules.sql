-- The classes of the paths of repos whose format classifies them per repo (format.EdgeSpec.Paths,
-- the overwrite rules of raw). rules is the JSON of a rule list, [[glob, immutable], ...].

-- Each rule list under which the outbox, a region fill or the reclass walk wrote a regional
-- pointer of the repo since the last walk ended: a regional pointer that is not pending exists
-- only for a path that one of them makes immutable, so the pre-steps make a path pending in every
-- bucket when any of them (or the current rules) says immutable. epoch is the walk epoch of the
-- repo at the last such write; the end of a walk deletes the lists not used since its start.
CREATE TABLE IF NOT EXISTS edge_paths (
  repo_id INTEGER NOT NULL,
  rules   TEXT NOT NULL,
  epoch   INTEGER NOT NULL,
  PRIMARY KEY (repo_id, rules)
) STRICT, WITHOUT ROWID;

-- The reclass walk of a repo (job edge.reclass). filled is the rule list that the regional
-- pointers follow: every path it makes immutable has its regional pointers ('' before a walk: the
-- format's globs). walking is the rule list of the walk that runs ('' for none) and cursor the
-- last path it did, so a walk resumes after a restart. epoch rises at each walk start.
CREATE TABLE IF NOT EXISTS edge_reclass (
  repo_id INTEGER PRIMARY KEY,
  epoch   INTEGER NOT NULL DEFAULT 0,
  filled  TEXT NOT NULL DEFAULT '',
  walking TEXT NOT NULL DEFAULT '',
  cursor  TEXT NOT NULL DEFAULT ''
) STRICT;
