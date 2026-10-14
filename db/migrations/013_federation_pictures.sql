-- Comment pictures from other servers are shown straight from the sender's
-- picture host, so the address of each picture is kept with the comment, and the
-- host each server serves its pictures from is remembered per server. Approved
-- servers' picture hosts are added to the img-src of the content policy.
ALTER TABLE remote_comments ADD COLUMN image_url TEXT NOT NULL DEFAULT '';
CREATE TABLE federation_media (
  host TEXT PRIMARY KEY,
  origin TEXT NOT NULL,
  checked_at TEXT NOT NULL
);
