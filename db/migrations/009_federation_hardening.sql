CREATE TABLE fed_seen (
  sig TEXT PRIMARY KEY,
  seen_at TEXT NOT NULL
);
CREATE INDEX idx_fed_seen_at ON fed_seen(seen_at);
