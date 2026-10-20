CREATE TABLE _reports_keep AS SELECT * FROM comment_reports;

CREATE TABLE comments_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  episode_id INTEGER REFERENCES episodes(id) ON DELETE CASCADE,
  profile_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  parent_id INTEGER REFERENCES comments_new(id) ON DELETE CASCADE,
  body TEXT NOT NULL,
  author_spoiler INTEGER NOT NULL DEFAULT 0,
  spoiler_reports INTEGER NOT NULL DEFAULT 0,
  not_spoiler_reports INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  edited_at TEXT,
  deleted_at TEXT,
  hidden_at TEXT,
  CHECK ((episode_id IS NULL) <> (profile_id IS NULL))
);
INSERT INTO comments_new (id, episode_id, user_id, parent_id, body, author_spoiler, spoiler_reports, not_spoiler_reports, created_at, edited_at, deleted_at)
  SELECT id, episode_id, user_id, parent_id, body, author_spoiler, spoiler_reports, not_spoiler_reports, created_at, edited_at, deleted_at FROM comments;
DROP TABLE comments;
ALTER TABLE comments_new RENAME TO comments;
INSERT INTO comment_reports SELECT * FROM _reports_keep;
DROP TABLE _reports_keep;

CREATE INDEX idx_comments_episode ON comments(episode_id, created_at);
CREATE INDEX idx_comments_profile ON comments(profile_id, created_at);
CREATE INDEX idx_comments_user ON comments(user_id);
CREATE INDEX idx_comments_parent ON comments(parent_id);
CREATE INDEX idx_comments_time ON comments(created_at);
