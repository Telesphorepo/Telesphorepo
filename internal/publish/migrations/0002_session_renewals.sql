CREATE TABLE IF NOT EXISTS publish_session_renewals (
  session_id INTEGER PRIMARY KEY REFERENCES publish_sessions(id) ON DELETE CASCADE,
  secret_hash BLOB NOT NULL
) STRICT;
