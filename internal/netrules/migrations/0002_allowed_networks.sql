-- The allowed networks of a repo: one list that grants downloads, where repo_ip_rules held a read
-- and a publish allow-list. The read rules carry over; the publish rules go. The table is copied
-- and the old one dropped in the one transaction of the migration. A public repo that had read
-- rules is made restricted by the network module at start (repos.Service), since this module
-- writes only its own tables.

-- An allowed network of a repo: a CIDR or the id of an IP list. A list in use cannot be deleted
-- (ON DELETE RESTRICT). repo_networks_repo reads the networks of a repo; repo_networks_list_id
-- finds the repos of a list.
CREATE TABLE IF NOT EXISTS repo_networks (
  id      INTEGER PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  cidr    TEXT,
  list_id INTEGER REFERENCES ip_lists(id) ON DELETE RESTRICT,
  note    TEXT NOT NULL DEFAULT '',
  CHECK ((cidr IS NULL) <> (list_id IS NULL))
) STRICT;
CREATE UNIQUE INDEX IF NOT EXISTS repo_networks_cidr ON repo_networks(repo_id, cidr) WHERE cidr IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS repo_networks_list ON repo_networks(repo_id, list_id) WHERE list_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS repo_networks_repo ON repo_networks(repo_id);
CREATE INDEX IF NOT EXISTS repo_networks_list_id ON repo_networks(list_id);

-- On a second run repo_ip_rules is gone: an empty stand-in lets the copy read nothing.
CREATE TABLE IF NOT EXISTS repo_ip_rules (
  id INTEGER PRIMARY KEY, repo_id INTEGER, scope TEXT, cidr TEXT, list_id INTEGER, note TEXT
);
INSERT OR IGNORE INTO repo_networks (id, repo_id, cidr, list_id, note)
  SELECT id, repo_id, cidr, list_id, note FROM repo_ip_rules WHERE scope = 'read';
DROP TABLE IF EXISTS repo_ip_rules;
