ALTER TABLE refresh_tokens ADD COLUMN sid TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_refresh_sid ON refresh_tokens(sid);
