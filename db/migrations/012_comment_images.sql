-- A comment or profile post can carry one picture or GIF. The value is the file
-- name of an upload under media/pics (empty when there is none).
ALTER TABLE comments ADD COLUMN image TEXT NOT NULL DEFAULT '';
