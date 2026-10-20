package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// FAQ is one question the support bot can answer without a person.
type FAQ struct {
	ID       int64
	Lang     string
	Position int
	Question string
	Answer   string
	Enabled  bool
}

func scanFAQ(row interface{ Scan(...any) error }) (*FAQ, error) {
	var f FAQ
	var on int
	if err := row.Scan(&f.ID, &f.Lang, &f.Position, &f.Question, &f.Answer, &on); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	f.Enabled = on == 1
	return &f, nil
}

const faqCols = `id, lang, position, question, answer, enabled`

// FAQList is the FAQ in one language ("" for every language), optionally only
// the enabled entries, in the order the operator set.
func (s *Store) FAQList(lang string, enabledOnly bool) ([]FAQ, error) {
	rows, err := s.DB.Query(`SELECT `+faqCols+` FROM support_faq WHERE (? = '' OR lang = ?) AND (? = 0 OR enabled = 1) ORDER BY lang, position, id`, lang, lang, boolInt(enabledOnly))
	if err != nil {
		return nil, fmt.Errorf("list support faq lang=%q: %w", lang, err)
	}
	defer rows.Close()
	var out []FAQ
	for rows.Next() {
		f, err := scanFAQ(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (s *Store) FAQGet(id int64) (*FAQ, error) {
	return scanFAQ(s.DB.QueryRow(`SELECT `+faqCols+` FROM support_faq WHERE id = ?`, id))
}

func (s *Store) FAQAdd(f FAQ) error {
	if _, err := s.DB.Exec(`INSERT INTO support_faq (lang, position, question, answer, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		f.Lang, f.Position, f.Question, f.Answer, boolInt(f.Enabled), Now(), Now()); err != nil {
		return fmt.Errorf("add support faq lang=%s: %w", f.Lang, err)
	}
	return nil
}

func (s *Store) FAQSave(f FAQ) error {
	if _, err := s.DB.Exec(`UPDATE support_faq SET position = ?, question = ?, answer = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		f.Position, f.Question, f.Answer, boolInt(f.Enabled), Now(), f.ID); err != nil {
		return fmt.Errorf("save support faq id=%d: %w", f.ID, err)
	}
	return nil
}

func (s *Store) FAQDelete(id int64) error {
	if _, err := s.DB.Exec(`DELETE FROM support_faq WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete support faq id=%d: %w", id, err)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
