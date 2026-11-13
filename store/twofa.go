package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

func twoFAPrefsKey(userID int64) string { return "twofa_prefs:" + strconv.FormatInt(userID, 10) }

func (s *Store) TwoFAPrefs(userID int64) (string, error) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, twoFAPrefsKey(userID)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read 2fa prefs uid=%d: %w", userID, err)
	}
	return v, nil
}

func (s *Store) SetTwoFAPrefs(userID int64, raw string) error {
	if err := s.SyncStateSet(twoFAPrefsKey(userID), raw); err != nil {
		return fmt.Errorf("save 2fa prefs uid=%d: %w", userID, err)
	}
	return nil
}
