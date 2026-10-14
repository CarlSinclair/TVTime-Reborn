-- Pictures that were uploaded but may never be attached to a comment. The
-- janitor deletes the ones still unused a day later.
CREATE TABLE pic_uploads (
  name TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_pic_uploads_created ON pic_uploads(created_at);
