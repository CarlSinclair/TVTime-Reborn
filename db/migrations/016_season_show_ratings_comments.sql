CREATE TABLE season_ratings (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  show_id INTEGER NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
  season_number INTEGER NOT NULL,
  rating INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 10),
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (user_id, show_id, season_number)
);
CREATE INDEX idx_season_ratings_show ON season_ratings(show_id, season_number);

CREATE TABLE show_ratings (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  show_id INTEGER NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
  rating INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 10),
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (user_id, show_id)
);
CREATE INDEX idx_show_ratings_show ON show_ratings(show_id);

CREATE TABLE remote_ratings (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('episode', 'season', 'show')),
  show_tmdb INTEGER NOT NULL,
  season INTEGER NOT NULL DEFAULT 0,
  episode INTEGER NOT NULL DEFAULT 0,
  rating INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 10),
  updated_at TEXT NOT NULL,
  PRIMARY KEY (user_id, kind, show_tmdb, season, episode)
);
CREATE INDEX idx_remote_ratings_show ON remote_ratings(show_tmdb, kind, season, episode);

CREATE TABLE _reports_keep AS SELECT * FROM comment_reports;

CREATE TABLE comments_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  episode_id INTEGER REFERENCES episodes(id) ON DELETE CASCADE,
  profile_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
  season_id INTEGER REFERENCES seasons(id) ON DELETE CASCADE,
  show_id INTEGER REFERENCES shows(id) ON DELETE CASCADE,
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
  parent_host TEXT NOT NULL DEFAULT '',
  parent_ref TEXT NOT NULL DEFAULT '',
  image TEXT NOT NULL DEFAULT '',
  CHECK ((episode_id IS NOT NULL) + (profile_id IS NOT NULL) + (season_id IS NOT NULL) + (show_id IS NOT NULL) = 1)
);
INSERT INTO comments_new (id, episode_id, profile_id, user_id, parent_id, body, author_spoiler, spoiler_reports, not_spoiler_reports, created_at, edited_at, deleted_at, hidden_at, parent_host, parent_ref, image)
  SELECT id, episode_id, profile_id, user_id, parent_id, body, author_spoiler, spoiler_reports, not_spoiler_reports, created_at, edited_at, deleted_at, hidden_at, parent_host, parent_ref, image FROM comments;
DROP TABLE comments;
ALTER TABLE comments_new RENAME TO comments;
INSERT INTO comment_reports SELECT * FROM _reports_keep;
DROP TABLE _reports_keep;

CREATE INDEX idx_comments_episode ON comments(episode_id, created_at);
CREATE INDEX idx_comments_profile ON comments(profile_id, created_at);
CREATE INDEX idx_comments_season ON comments(season_id, created_at);
CREATE INDEX idx_comments_show ON comments(show_id, created_at);
CREATE INDEX idx_comments_user ON comments(user_id);
CREATE INDEX idx_comments_parent ON comments(parent_id);
CREATE INDEX idx_comments_time ON comments(created_at);
