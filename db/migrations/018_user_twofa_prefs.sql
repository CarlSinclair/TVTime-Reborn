CREATE TABLE user_twofa_prefs (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  prefs TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO user_twofa_prefs (user_id, prefs)
SELECT CAST(substr(key, 13) AS INTEGER), value FROM sync_state
WHERE key LIKE 'twofa_prefs:%' AND CAST(substr(key, 13) AS INTEGER) IN (SELECT id FROM users);
DELETE FROM sync_state WHERE key LIKE 'twofa_prefs:%';
