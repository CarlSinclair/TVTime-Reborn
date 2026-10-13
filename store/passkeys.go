package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
)

type PasskeyRow struct {
	ID         int64
	Name       string
	CreatedAt  string
	LastUsed   string
	CredID     []byte
	Credential string
}

// WebAuthnHandle returns the stable, random, non-identifying user handle
// WebAuthn needs for discoverable credentials, creating it on first use.
func (s *Store) WebAuthnHandle(userID int64) ([]byte, error) {
	var h []byte
	err := s.DB.QueryRow(`SELECT webauthn_id FROM users WHERE id = ?`, userID).Scan(&h)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read webauthn handle uid=%d: %w", userID, err)
	}
	if len(h) > 0 {
		return h, nil
	}
	h = make([]byte, 32)
	if _, err := rand.Read(h); err != nil {
		return nil, fmt.Errorf("webauthn handle entropy: %w", err)
	}
	res, err := s.DB.Exec(`UPDATE users SET webauthn_id = ? WHERE id = ? AND (webauthn_id IS NULL OR length(webauthn_id) = 0)`, h, userID)
	if err != nil {
		return nil, fmt.Errorf("assign webauthn handle uid=%d: %w", userID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return s.WebAuthnHandle(userID)
	}
	return h, nil
}

func (s *Store) UserByWebAuthnHandle(h []byte) (*User, error) {
	if len(h) == 0 {
		return nil, ErrNotFound
	}
	return scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE webauthn_id = ?`, h))
}

func (s *Store) Passkeys(userID int64) ([]PasskeyRow, error) {
	rows, err := s.DB.Query(`SELECT id, cred_id, credential, name, created_at, last_used FROM passkeys WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list passkeys uid=%d: %w", userID, err)
	}
	defer rows.Close()
	var out []PasskeyRow
	for rows.Next() {
		var p PasskeyRow
		if err := rows.Scan(&p.ID, &p.CredID, &p.Credential, &p.Name, &p.CreatedAt, &p.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) AddPasskey(userID int64, credID []byte, credential, name string) error {
	if _, err := s.DB.Exec(`INSERT INTO passkeys (user_id, cred_id, credential, name, created_at) VALUES (?,?,?,?,?)`, userID, credID, credential, name, Now()); err != nil {
		return fmt.Errorf("add passkey uid=%d: %w", userID, err)
	}
	return nil
}

func (s *Store) UpdatePasskeyUse(userID int64, credID []byte, credential string) error {
	res, err := s.DB.Exec(`UPDATE passkeys SET credential = ?, last_used = ? WHERE user_id = ? AND cred_id = ?`, credential, Now(), userID, credID)
	if err != nil {
		return fmt.Errorf("update passkey use uid=%d: %w", userID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeletePasskey(userID, id int64) (bool, error) {
	res, err := s.DB.Exec(`DELETE FROM passkeys WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, fmt.Errorf("delete passkey id=%d uid=%d: %w", id, userID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) CountPasskeys(userID int64) (int, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM passkeys WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count passkeys uid=%d: %w", userID, err)
	}
	return n, nil
}
