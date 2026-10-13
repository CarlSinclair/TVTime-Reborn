-- Support conversations run through the Telegram bot: a visitor talks to the bot,
-- the operator answers by replying to the messages the bot relays to them.
CREATE TABLE support_threads (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  lang TEXT NOT NULL DEFAULT 'en',
  state TEXT NOT NULL CHECK (state IN ('name','issue','open','closed')),
  user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_support_chat ON support_threads(chat_id, state);

CREATE TABLE support_relay (
  operator_chat TEXT NOT NULL,
  message_id INTEGER NOT NULL,
  thread_id INTEGER NOT NULL REFERENCES support_threads(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  PRIMARY KEY (operator_chat, message_id)
);
