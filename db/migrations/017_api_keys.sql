CREATE TABLE api_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key_hash TEXT NOT NULL UNIQUE,
  key_prefix TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  scopes TEXT NOT NULL,
  rate_limit INTEGER NOT NULL DEFAULT 0,
  created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  last_used_at TEXT,
  last_used_ip TEXT NOT NULL DEFAULT '',
  expires_at TEXT,
  revoked_at TEXT
);
CREATE INDEX idx_api_keys_owner ON api_keys(created_by, revoked_at);
