-- Replies across servers. A local comment can answer a comment that lives on another
-- server (parent_host + parent_ref instead of parent_id), and a copy of another server's
-- comment can answer a comment on any server, including this one.
ALTER TABLE comments ADD COLUMN parent_host TEXT NOT NULL DEFAULT '';
ALTER TABLE comments ADD COLUMN parent_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE remote_comments ADD COLUMN parent_host TEXT NOT NULL DEFAULT '';
