-- Questions and answers the support bot offers before (and next to) the
-- "Talk to a human" button. One row per question and language.
CREATE TABLE support_faq (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  lang TEXT NOT NULL DEFAULT 'en',
  position INTEGER NOT NULL DEFAULT 0,
  question TEXT NOT NULL,
  answer TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_support_faq_lang ON support_faq(lang, enabled, position);
