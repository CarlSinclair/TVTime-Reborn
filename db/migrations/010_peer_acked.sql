ALTER TABLE federation_peers ADD COLUMN acked INTEGER NOT NULL DEFAULT 0;
UPDATE federation_peers SET acked = 1;
