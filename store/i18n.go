package store

import (
	"fmt"
	"strings"
)

type ShowText struct{ Name, Overview string }
type EpisodeText struct{ Name, Overview string }

type EpisodeI18nInput struct {
	Season, Number int
	Name, Overview string
}

func inClause(ids []int64) (string, []any) {
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args[i] = id
	}
	return strings.Join(marks, ","), args
}

func (s *Store) ShowI18nAge(showID int64, lang string) (exists bool, syncedAt string, err error) {
	rows, err := s.DB.Query(`SELECT synced_at FROM show_i18n WHERE show_id = ? AND lang = ?`, showID, lang)
	if err != nil {
		return false, "", fmt.Errorf("show i18n age show=%d lang=%s: %w", showID, lang, err)
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&syncedAt); err != nil {
			return false, "", err
		}
		return true, syncedAt, nil
	}
	return false, "", rows.Err()
}

func (s *Store) SaveShowI18n(showID int64, lang, name, overview string, eps []EpisodeI18nInput) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin save show i18n show=%d: %w", showID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO show_i18n (show_id, lang, name, overview, synced_at) VALUES (?,?,?,?,?)
	  ON CONFLICT(show_id, lang) DO UPDATE SET name=excluded.name, overview=excluded.overview, synced_at=excluded.synced_at`, showID, lang, name, overview, Now()); err != nil {
		return fmt.Errorf("save show i18n show=%d lang=%s: %w", showID, lang, err)
	}
	for _, e := range eps {
		if _, err := tx.Exec(`INSERT INTO episode_i18n (episode_id, lang, name, overview)
		  SELECT id, ?, ?, ? FROM episodes WHERE show_id = ? AND season_number = ? AND episode_number = ?
		  ON CONFLICT(episode_id, lang) DO UPDATE SET name=excluded.name, overview=excluded.overview`, lang, e.Name, e.Overview, showID, e.Season, e.Number); err != nil {
			return fmt.Errorf("save episode i18n show=%d s%de%d lang=%s: %w", showID, e.Season, e.Number, lang, err)
		}
	}
	return tx.Commit()
}

func (s *Store) ShowTexts(lang string, ids []int64) (map[int64]ShowText, error) {
	out := map[int64]ShowText{}
	if lang == "en" || len(ids) == 0 {
		return out, nil
	}
	in, args := inClause(ids)
	rows, err := s.DB.Query(`SELECT show_id, name, overview FROM show_i18n WHERE lang = ? AND show_id IN (`+in+`)`, append([]any{lang}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("show texts lang=%s: %w", lang, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t ShowText
		if err := rows.Scan(&id, &t.Name, &t.Overview); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

func (s *Store) EpisodeTexts(lang string, ids []int64) (map[int64]EpisodeText, error) {
	out := map[int64]EpisodeText{}
	if lang == "en" || len(ids) == 0 {
		return out, nil
	}
	in, args := inClause(ids)
	rows, err := s.DB.Query(`SELECT episode_id, name, overview FROM episode_i18n WHERE lang = ? AND episode_id IN (`+in+`)`, append([]any{lang}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("episode texts lang=%s: %w", lang, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t EpisodeText
		if err := rows.Scan(&id, &t.Name, &t.Overview); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

// LocalizeShows overwrites name/overview on the given shows with the stored
// translation when one exists and is non-empty, otherwise leaves the English
// text TMDB returned as the canonical source.
func (s *Store) LocalizeShows(lang string, shows ...*Show) error {
	ids := make([]int64, 0, len(shows))
	for _, sh := range shows {
		ids = append(ids, sh.ID)
	}
	texts, err := s.ShowTexts(lang, ids)
	if err != nil {
		return err
	}
	for _, sh := range shows {
		if t, ok := texts[sh.ID]; ok {
			if strings.TrimSpace(t.Name) != "" {
				sh.Name = t.Name
			}
			if strings.TrimSpace(t.Overview) != "" {
				sh.Overview = t.Overview
			}
		}
	}
	return nil
}

func (s *Store) LocalizeEpisodes(lang string, eps ...*Episode) error {
	ids := make([]int64, 0, len(eps))
	for _, e := range eps {
		ids = append(ids, e.ID)
	}
	texts, err := s.EpisodeTexts(lang, ids)
	if err != nil {
		return err
	}
	for _, e := range eps {
		if t, ok := texts[e.ID]; ok {
			if strings.TrimSpace(t.Name) != "" {
				e.Name = t.Name
			}
			if strings.TrimSpace(t.Overview) != "" {
				e.Overview = t.Overview
			}
		}
	}
	return nil
}

type LangDue struct {
	ShowID, TMDBID int64
	Lang           string
}

func (s *Store) LocalizationsDue(staleBefore string, limit int) ([]LangDue, error) {
	rows, err := s.DB.Query(`SELECT DISTINCT sh.id, sh.tmdb_id, u.locale
	  FROM follows f
	  JOIN users u ON u.id = f.user_id
	  JOIN shows sh ON sh.id = f.show_id
	  LEFT JOIN show_i18n i ON i.show_id = sh.id AND i.lang = u.locale
	  WHERE u.locale <> 'en' AND sh.tmdb_id > 0 AND u.is_banned = 0 AND (i.synced_at IS NULL OR i.synced_at < ?)
	  ORDER BY sh.id LIMIT ?`, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list localizations due: %w", err)
	}
	defer rows.Close()
	var out []LangDue
	for rows.Next() {
		var d LangDue
		if err := rows.Scan(&d.ShowID, &d.TMDBID, &d.Lang); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
