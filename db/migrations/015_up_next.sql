-- The home page's "up next" list, kept up to date as people watch and as
-- episodes arrive instead of being worked out on every visit. One row per person
-- and followed show that has something left to watch: the next episode, how many
-- remain, and when the person last touched the show (the list order).
CREATE TABLE up_next (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  show_id INTEGER NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
  next_episode_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
  remaining INTEGER NOT NULL,
  sort_at TEXT NOT NULL,
  PRIMARY KEY (user_id, show_id)
);
CREATE INDEX idx_up_next_order ON up_next(user_id, sort_at DESC);
