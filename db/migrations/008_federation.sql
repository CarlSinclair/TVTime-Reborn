ALTER TABLE users ADD COLUMN remote_host TEXT NOT NULL DEFAULT '';

CREATE TABLE federation_peers (
  host TEXT PRIMARY KEY,
  public_key TEXT NOT NULL,
  first_seen TEXT NOT NULL,
  last_seen TEXT NOT NULL,
  blocked INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE fed_outbox (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  host TEXT NOT NULL,
  path TEXT NOT NULL,
  body TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_outbox_due ON fed_outbox(next_at);

CREATE TABLE remote_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  ref TEXT NOT NULL,
  kind TEXT NOT NULL,
  show_tmdb INTEGER NOT NULL DEFAULT 0,
  show_name TEXT NOT NULL DEFAULT '',
  poster TEXT NOT NULL DEFAULT '',
  season INTEGER NOT NULL DEFAULT 0,
  episode INTEGER NOT NULL DEFAULT 0,
  count INTEGER NOT NULL DEFAULT 1,
  stars INTEGER NOT NULL DEFAULT 0,
  excerpt TEXT NOT NULL DEFAULT '',
  at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  UNIQUE(user_id, ref)
);
CREATE INDEX idx_remote_events_user ON remote_events(user_id, at);
CREATE INDEX idx_remote_events_exp ON remote_events(expires_at);
