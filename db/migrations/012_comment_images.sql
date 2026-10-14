-- Pictures on comments: a comment or profile post can carry one picture or GIF.
-- comments.image is the file name of an upload under media/pics (empty when there
-- is none). pic_uploads remembers every upload so that ones no comment ever used
-- are deleted a day later.
ALTER TABLE comments ADD COLUMN image TEXT NOT NULL DEFAULT '';
CREATE TABLE pic_uploads (
  name TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_pic_uploads_created ON pic_uploads(created_at);
