package store

import (
	"database/sql"

	"github.com/CarlSinclair/csuit-libs/twofa"
)

func (s *Store) twoFAPrefsStore() twofa.SQLPrefs {
	return twofa.SQLPrefs{DB: func() *sql.DB { return s.DB }, SQLite: true}
}

func (s *Store) TwoFAPrefs(userID int64) (twofa.Prefs, error) {
	return s.twoFAPrefsStore().Load(userID)
}

func (s *Store) SetTwoFAPrefs(userID int64, p twofa.Prefs) error {
	return s.twoFAPrefsStore().Save(userID, p)
}
