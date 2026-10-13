package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	SourceTVmaze = "tvmaze"
	SourceTrakt  = "trakt"
)

type AirTime struct {
	Season, Number int
	AirDate        string
	AirAt          time.Time
}

// ProviderState returns the stored provider id (0 if none) and when it was last
// looked up. provider is "tvmaze" or "trakt".
func (s *Store) ProviderState(showID int64, provider string) (id int64, checkedAt string, err error) {
	var q string
	switch provider {
	case SourceTVmaze:
		q = `SELECT tvmaze_id, tvmaze_checked_at FROM shows WHERE id = ?`
	case SourceTrakt:
		q = `SELECT trakt_id, trakt_checked_at FROM shows WHERE id = ?`
	default:
		return 0, "", fmt.Errorf("unknown provider %q", provider)
	}
	var chk sql.NullString
	var pid sql.NullInt64
	if err := s.DB.QueryRow(q, showID).Scan(&pid, &chk); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", ErrNotFound
		}
		return 0, "", fmt.Errorf("%s state show=%d: %w", provider, showID, err)
	}
	return pid.Int64, chk.String, nil
}

func (s *Store) MarkProviderChecked(showID int64, provider string) error {
	col := "tvmaze_checked_at"
	if provider == SourceTrakt {
		col = "trakt_checked_at"
	}
	if _, err := s.DB.Exec(`UPDATE shows SET `+col+` = ? WHERE id = ?`, Now(), showID); err != nil {
		return fmt.Errorf("mark %s checked show=%d: %w", provider, showID, err)
	}
	return nil
}

// SaveAirTimes records broadcast times from one provider. TVmaze is
// authoritative and overwrites; Trakt only fills episodes that have no time.
// An entry applies only when its season/episode exists and its date is within a
// day of the TMDB date, so numbering differences between catalogues cannot
// attach a time to the wrong episode.
func (s *Store) SaveAirTimes(showID int64, provider string, providerID int64, times []AirTime) (int, error) {
	idCol, chkCol, guard := "tvmaze_id", "tvmaze_checked_at", ""
	if provider == SourceTrakt {
		idCol, chkCol, guard = "trakt_id", "trakt_checked_at", " AND air_at IS NULL"
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin save air times show=%d: %w", showID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE shows SET `+idCol+` = ?, `+chkCol+` = ? WHERE id = ?`, providerID, Now(), showID); err != nil {
		return 0, fmt.Errorf("save %s id show=%d: %w", provider, showID, err)
	}
	applied := 0
	for _, t := range times {
		res, err := tx.Exec(`UPDATE episodes SET air_at = ?, air_source = ? WHERE show_id = ? AND season_number = ? AND episode_number = ?
		  AND air_date IS NOT NULL AND ABS(julianday(air_date) - julianday(?)) <= 1`+guard,
			Ts(t.AirAt), provider, showID, t.Season, t.Number, t.AirDate)
		if err != nil {
			return 0, fmt.Errorf("save air time show=%d s%de%d: %w", showID, t.Season, t.Number, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			applied++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit air times show=%d: %w", showID, err)
	}
	return applied, nil
}

// HasUntimedRecent reports whether a show has an episode around now (recent or
// upcoming) with no exact air time yet, i.e. whether a second source could help.
func (s *Store) HasUntimedRecent(showID int64, now time.Time) (bool, error) {
	from := now.UTC().AddDate(0, 0, -3).Format("2006-01-02")
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM episodes WHERE show_id = ? AND season_number > 0 AND air_at IS NULL AND air_date >= ?`, showID, from).Scan(&n); err != nil {
		return false, fmt.Errorf("untimed episodes show=%d: %w", showID, err)
	}
	return n > 0, nil
}
