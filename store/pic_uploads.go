package store

import "fmt"

// PicUploadAdd remembers a freshly uploaded picture so it can be removed if no
// comment ever uses it.
func (s *Store) PicUploadAdd(name string, userID int64) error {
	if _, err := s.DB.Exec(`INSERT INTO pic_uploads (name, user_id, created_at) VALUES (?, ?, ?)
	  ON CONFLICT(name) DO UPDATE SET created_at = excluded.created_at`, name, userID, Now()); err != nil {
		return fmt.Errorf("record picture upload %s: %w", name, err)
	}
	return nil
}

// PicUploadsBefore lists the uploads recorded before the given time.
func (s *Store) PicUploadsBefore(cutoff string) ([]string, error) {
	rows, err := s.DB.Query(`SELECT name FROM pic_uploads WHERE created_at < ? ORDER BY created_at LIMIT 500`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("list old picture uploads: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) PicUploadForget(name string) error {
	if _, err := s.DB.Exec(`DELETE FROM pic_uploads WHERE name = ?`, name); err != nil {
		return fmt.Errorf("forget picture upload %s: %w", name, err)
	}
	return nil
}
