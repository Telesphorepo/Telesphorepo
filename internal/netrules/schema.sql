-- Network module schema: IP lists, per-repo IP rules and the IP lists that trusted proxies
-- include. References repos of the kernel schema. Request paths read the in-memory snapshot of
-- the module, never these tables.

-- Named IP lists. A 'preset' list (cloudflare, github-actions) is fetched from source and
-- refreshed daily: ranges is the last list that fetched and parsed, fetched_at the unix time of
-- that fetch (0: never) and error the last refresh error ('' after a good refresh). A 'custom'
-- list holds the ranges an admin entered. ranges is a JSON array of CIDRs. Rules and trusted
-- proxies name a list by its id, so a rename changes one row.
CREATE TABLE ip_lists (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  kind        TEXT NOT NULL CHECK (kind IN ('preset', 'custom')),
  title       TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  ranges      TEXT NOT NULL DEFAULT '[]',
  source      TEXT NOT NULL DEFAULT '',
  fetched_at  INTEGER NOT NULL DEFAULT 0,
  error       TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
) STRICT;

-- Per-repo IP allow-lists. A scope without rows is unrestricted. A rule holds a CIDR or the id
-- of an IP list; a list in use cannot be deleted (ON DELETE RESTRICT). repo_ip_rules_list_id finds
-- the rules of a list.
CREATE TABLE repo_ip_rules (
  id      INTEGER PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  scope   TEXT NOT NULL CHECK (scope IN ('read','publish')),
  cidr    TEXT,
  list_id INTEGER REFERENCES ip_lists(id) ON DELETE RESTRICT,
  note    TEXT NOT NULL DEFAULT '',
  CHECK ((cidr IS NULL) <> (list_id IS NULL))
) STRICT;
CREATE UNIQUE INDEX repo_ip_rules_cidr ON repo_ip_rules(repo_id, scope, cidr) WHERE cidr IS NOT NULL;
CREATE UNIQUE INDEX repo_ip_rules_list ON repo_ip_rules(repo_id, scope, list_id) WHERE list_id IS NOT NULL;
CREATE INDEX repo_ip_rules_repo ON repo_ip_rules(repo_id, scope);
CREATE INDEX repo_ip_rules_list_id ON repo_ip_rules(list_id);

-- The IP lists that trusted proxies include. The setting trusted_proxies holds the literal
-- entries; the "list:<name>" entries of TR_TRUSTED_PROXIES are resolved by name at startup and
-- are not rows.
CREATE TABLE trusted_proxy_lists (
  list_id INTEGER PRIMARY KEY REFERENCES ip_lists(id) ON DELETE RESTRICT
) STRICT;
