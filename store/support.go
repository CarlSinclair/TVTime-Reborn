package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// SupportThread is one conversation between a visitor and the operator that runs
// through the Telegram bot.
type SupportThread struct {
	ID     int64
	ChatID string
	Name   string
	Lang   string
	State  string
	UserID int64
}

const supportCols = `id, chat_id, name, lang, state, COALESCE(user_id, 0)`

func scanSupport(row interface{ Scan(...any) error }) (*SupportThread, error) {
	var t SupportThread
	if err := row.Scan(&t.ID, &t.ChatID, &t.Name, &t.Lang, &t.State, &t.UserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// StartSupportThread begins a new conversation for a chat, closing any that was
// still open, and leaves it waiting for the person's name.
func (s *Store) StartSupportThread(chat, lang string, userID int64) (*SupportThread, error) {
	if _, err := s.DB.Exec(`UPDATE support_threads SET state = 'closed', updated_at = ? WHERE chat_id = ? AND state <> 'closed'`, Now(), chat); err != nil {
		return nil, fmt.Errorf("close old support threads chat=%s: %w", chat, err)
	}
	var uid any
	if userID != 0 {
		uid = userID
	}
	res, err := s.DB.Exec(`INSERT INTO support_threads (chat_id, lang, state, user_id, created_at, updated_at) VALUES (?, ?, 'name', ?, ?, ?)`, chat, lang, uid, Now(), Now())
	if err != nil {
		return nil, fmt.Errorf("start support thread chat=%s: %w", chat, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return scanSupport(s.DB.QueryRow(`SELECT `+supportCols+` FROM support_threads WHERE id = ?`, id))
}

// ActiveSupportThread is the chat's conversation that is not closed, if any.
func (s *Store) ActiveSupportThread(chat string) (*SupportThread, error) {
	return scanSupport(s.DB.QueryRow(`SELECT `+supportCols+` FROM support_threads WHERE chat_id = ? AND state <> 'closed' ORDER BY id DESC LIMIT 1`, chat))
}

func (s *Store) SupportThreadByID(id int64) (*SupportThread, error) {
	return scanSupport(s.DB.QueryRow(`SELECT `+supportCols+` FROM support_threads WHERE id = ?`, id))
}

func (s *Store) SetSupportName(id int64, name string) error {
	if _, err := s.DB.Exec(`UPDATE support_threads SET name = ?, state = 'issue', updated_at = ? WHERE id = ?`, name, Now(), id); err != nil {
		return fmt.Errorf("set support name thread=%d: %w", id, err)
	}
	return nil
}

func (s *Store) SetSupportState(id int64, state string) error {
	if _, err := s.DB.Exec(`UPDATE support_threads SET state = ?, updated_at = ? WHERE id = ?`, state, Now(), id); err != nil {
		return fmt.Errorf("set support state thread=%d: %w", id, err)
	}
	return nil
}

// AddSupportRelay remembers that a message the bot sent to the operator belongs
// to a conversation, so the operator's reply to it can be routed back.
func (s *Store) AddSupportRelay(operatorChat string, messageID, threadID int64) error {
	if _, err := s.DB.Exec(`INSERT OR REPLACE INTO support_relay (operator_chat, message_id, thread_id, created_at) VALUES (?, ?, ?, ?)`, operatorChat, messageID, threadID, Now()); err != nil {
		return fmt.Errorf("add support relay thread=%d: %w", threadID, err)
	}
	return nil
}

func (s *Store) SupportThreadByRelay(operatorChat string, messageID int64) (*SupportThread, error) {
	return scanSupport(s.DB.QueryRow(`SELECT t.id, t.chat_id, t.name, t.lang, t.state, COALESCE(t.user_id, 0)
	  FROM support_relay r JOIN support_threads t ON t.id = r.thread_id WHERE r.operator_chat = ? AND r.message_id = ?`, operatorChat, messageID))
}

// OperatorChats are the Telegram chats support messages are relayed to: every
// admin who has connected the bot.
func (s *Store) OperatorChats() ([]string, error) {
	rows, err := s.DB.Query(`SELECT telegram_chat_id FROM users WHERE is_admin = 1 AND is_deactivated = 0 AND telegram_chat_id <> ''`)
	if err != nil {
		return nil, fmt.Errorf("operator chats: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PurgeSupport removes closed conversations and relay records older than the
// retention period.
func (s *Store) PurgeSupport(olderThan string) error {
	if _, err := s.DB.Exec(`DELETE FROM support_threads WHERE state = 'closed' AND updated_at < ?`, olderThan); err != nil {
		return fmt.Errorf("purge support threads: %w", err)
	}
	if _, err := s.DB.Exec(`DELETE FROM support_relay WHERE created_at < ?`, olderThan); err != nil {
		return fmt.Errorf("purge support relay: %w", err)
	}
	return nil
}
