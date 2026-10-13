ALTER TABLE import_jobs ADD COLUMN payload TEXT NOT NULL DEFAULT '';
ALTER TABLE import_jobs ADD COLUMN shows_done INTEGER NOT NULL DEFAULT 0;
ALTER TABLE import_jobs ADD COLUMN episodes_added INTEGER NOT NULL DEFAULT 0;
ALTER TABLE import_jobs ADD COLUMN episodes_skipped INTEGER NOT NULL DEFAULT 0;
ALTER TABLE import_jobs ADD COLUMN follows_added INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_import_jobs_user ON import_jobs(user_id, id);
