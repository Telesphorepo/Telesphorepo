-- The audit events of one repo and one action, newest first (the repo delete, the repo activity).
CREATE INDEX audit_events_repo_action ON audit_events(repo_id, action, id);