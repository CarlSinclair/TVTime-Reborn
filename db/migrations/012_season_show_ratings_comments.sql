-- +goose Up
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
