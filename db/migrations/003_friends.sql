CREATE TABLE friend_requests (
  requester_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  addressee_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','accepted')),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  responded_at TEXT,
  PRIMARY KEY (requester_id, addressee_id)
);
CREATE INDEX idx_friend_addressee ON friend_requests(addressee_id, status);
ALTER TABLE users ADD COLUMN share_activity INTEGER NOT NULL DEFAULT 1;
CREATE INDEX idx_watched_time ON watched(user_id, watched_at);
CREATE INDEX idx_ratings_time ON episode_ratings(user_id, updated_at);
CREATE INDEX idx_comments_time ON comments(created_at);
