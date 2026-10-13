ALTER TABLE users ADD COLUMN remote_host TEXT NOT NULL DEFAULT '';

CREATE TABLE federation_peers (
  host TEXT PRIMARY KEY,
  public_key TEXT NOT NULL,
  first_seen TEXT NOT NULL,
  last_seen TEXT NOT NULL,
  blocked INTEGER NOT NULL DEFAULT 0,
  acked INTEGER NOT NULL DEFAULT 0,
  share INTEGER NOT NULL DEFAULT 0
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

CREATE TABLE fed_seen (
  sig TEXT PRIMARY KEY,
  seen_at TEXT NOT NULL
);
CREATE INDEX idx_fed_seen_at ON fed_seen(seen_at);

CREATE TABLE username_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  old_name TEXT NOT NULL COLLATE NOCASE,
  changed_at TEXT NOT NULL
);
CREATE INDEX idx_username_history_name ON username_history(old_name);
CREATE INDEX idx_username_history_user ON username_history(user_id);

CREATE TABLE remote_comments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  host TEXT NOT NULL,
  ref TEXT NOT NULL,
  parent_ref TEXT NOT NULL DEFAULT '',
  show_tmdb INTEGER NOT NULL,
  season INTEGER NOT NULL,
  episode INTEGER NOT NULL,
  body TEXT NOT NULL,
  spoiler INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  edited_at TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL,
  UNIQUE(host, ref)
);
CREATE INDEX idx_remote_comments_episode ON remote_comments(show_tmdb, season, episode);
CREATE INDEX idx_remote_comments_exp ON remote_comments(expires_at);
