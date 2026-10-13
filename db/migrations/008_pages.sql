CREATE TABLE pages (
  page_id      INTEGER NOT NULL,
  lang         TEXT NOT NULL,
  slug         TEXT NOT NULL DEFAULT '',
  title        TEXT NOT NULL DEFAULT '',
  body         TEXT NOT NULL DEFAULT '',
  enabled      INTEGER NOT NULL DEFAULT 1,
  page_enabled INTEGER NOT NULL DEFAULT 1,
  show_updated INTEGER NOT NULL DEFAULT 0,
  updated_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_by   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (page_id, lang)
);
CREATE UNIQUE INDEX idx_pages_slug ON pages(slug) WHERE slug != '';
