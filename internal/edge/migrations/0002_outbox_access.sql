-- The due order of the access objects (repo_id 0), which the outbox worker reads before the
-- pointers.
CREATE INDEX edge_outbox_access ON edge_outbox(repo_id, due_at) WHERE repo_id = 0;