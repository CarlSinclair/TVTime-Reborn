package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"tvt/logger"
)

// The up_next table holds each person's home-page list. It is not rebuilt per
// visit: every change that can alter one row (watching, unwatching, following,
// a show's episodes being synced, an episode airing) updates just that row, a
// start-up pass fills the table the first time, and a daily pass re-checks
// everything as a safety net.

const (
	upNextBuiltKey = "upnext_built"
	upNextDayKey   = "upnext_day"
)

// refreshUpNext recomputes one person's row for one show from the source tables.
// Failures are logged and leave the old row for the daily pass to fix.
func (s *Store) refreshUpNext(userID, showID int64) {
	if userID == 0 || showID == 0 {
		return
	}
	if err := s.refreshUpNextErr(userID, showID); err != nil {
		logger.Error("UPNEXT refresh uid=%d show=%d: %v", userID, showID, err)
	}
}

func (s *Store) refreshUpNextOf(userID, episodeID int64) {
	var showID int64
	if err := s.DB.QueryRow(`SELECT show_id FROM episodes WHERE id = ?`, episodeID).Scan(&showID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logger.Error("UPNEXT show of episode=%d: %v", episodeID, err)
		}
		return
	}
	s.refreshUpNext(userID, showID)
}

func (s *Store) refreshUpNextErr(userID, showID int64) error {
	remove := func() error {
		if _, err := s.DB.Exec(`DELETE FROM up_next WHERE user_id = ? AND show_id = ?`, userID, showID); err != nil {
			return fmt.Errorf("remove row: %w", err)
		}
		return nil
	}
	var status, created string
	err := s.DB.QueryRow(`SELECT status, created_at FROM follows WHERE user_id = ? AND show_id = ?`, userID, showID).Scan(&status, &created)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "watching") {
		return remove()
	}
	if err != nil {
		return fmt.Errorf("read follow: %w", err)
	}
	var next sql.NullInt64
	var remaining int
	err = s.DB.QueryRow(`SELECT
	    (SELECT e.id FROM episodes e WHERE e.show_id = ?1 AND e.season_number > 0 AND e.air_date IS NOT NULL AND e.air_date <= ?3
	       AND NOT EXISTS (SELECT 1 FROM watched w WHERE w.user_id = ?2 AND w.episode_id = e.id)
	     ORDER BY e.season_number, e.episode_number LIMIT 1),
	    (SELECT COUNT(*) FROM episodes e WHERE e.show_id = ?1 AND e.season_number > 0 AND e.air_date IS NOT NULL AND e.air_date <= ?3
	       AND NOT EXISTS (SELECT 1 FROM watched w WHERE w.user_id = ?2 AND w.episode_id = e.id))`,
		showID, userID, today()).Scan(&next, &remaining)
	if err != nil {
		return fmt.Errorf("find next episode: %w", err)
	}
	if !next.Valid {
		return remove()
	}
	sortAt := created
	var last sql.NullString
	if err := s.DB.QueryRow(`SELECT MAX(watched_at) FROM watched WHERE user_id = ? AND show_id = ?`, userID, showID).Scan(&last); err != nil {
		return fmt.Errorf("read last watched: %w", err)
	}
	if last.Valid && last.String != "" {
		sortAt = last.String
	}
	if _, err := s.DB.Exec(`INSERT INTO up_next (user_id, show_id, next_episode_id, remaining, sort_at) VALUES (?,?,?,?,?)
	  ON CONFLICT(user_id, show_id) DO UPDATE SET next_episode_id = excluded.next_episode_id, remaining = excluded.remaining, sort_at = excluded.sort_at`,
		userID, showID, next.Int64, remaining, sortAt); err != nil {
		return fmt.Errorf("write row: %w", err)
	}
	return nil
}

// refreshUpNextShow recomputes a show's row for everyone who is watching it, for
// when its episodes change.
func (s *Store) refreshUpNextShow(showID int64) {
	rows, err := s.DB.Query(`SELECT user_id FROM follows WHERE show_id = ? AND status = 'watching'`, showID)
	if err != nil {
		logger.Error("UPNEXT followers of show=%d: %v", showID, err)
		return
	}
	var users []int64
	for rows.Next() {
		var u int64
		if err := rows.Scan(&u); err != nil {
			rows.Close()
			logger.Error("UPNEXT followers of show=%d: %v", showID, err)
			return
		}
		users = append(users, u)
	}
	rows.Close()
	for _, u := range users {
		s.refreshUpNext(u, showID)
	}
}

// dropUpNextOfInactive removes rows for follows that are no longer being
// watched (a show moved to completed, say).
func (s *Store) dropUpNextOfInactive(userID, showID int64) {
	if _, err := s.DB.Exec(`DELETE FROM up_next WHERE (? = 0 OR user_id = ?) AND (? = 0 OR show_id = ?)
	  AND NOT EXISTS (SELECT 1 FROM follows f WHERE f.user_id = up_next.user_id AND f.show_id = up_next.show_id AND f.status = 'watching')`,
		userID, userID, showID, showID); err != nil {
		logger.Error("UPNEXT drop inactive uid=%d show=%d: %v", userID, showID, err)
	}
}

// RebuildUpNext recomputes every row from scratch. It is the first-run fill and
// the daily safety net; it never empties the table, it corrects it in place.
func (s *Store) RebuildUpNext() error {
	rows, err := s.DB.Query(`SELECT user_id, show_id FROM follows WHERE status = 'watching'`)
	if err != nil {
		return fmt.Errorf("list watched follows: %w", err)
	}
	type pair struct{ u, sh int64 }
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.u, &p.sh); err != nil {
			rows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	failed := 0
	for _, p := range pairs {
		if err := s.refreshUpNextErr(p.u, p.sh); err != nil {
			failed++
			logger.Error("UPNEXT rebuild uid=%d show=%d: %v", p.u, p.sh, err)
		}
	}
	s.dropUpNextOfInactive(0, 0)
	if failed > 0 {
		return fmt.Errorf("%d of %d rows could not be rebuilt", failed, len(pairs))
	}
	return nil
}

// EnsureUpNext fills the table the first time the server runs with it.
func (s *Store) EnsureUpNext() error {
	if s.KVGet(upNextBuiltKey) != "" {
		return nil
	}
	start := time.Now()
	if err := s.RebuildUpNext(); err != nil {
		return err
	}
	if err := s.KVSet(upNextBuiltKey, Now()); err != nil {
		return err
	}
	if err := s.KVSet(upNextDayKey, today()); err != nil {
		return err
	}
	logger.Info("UPNEXT built the up-next table in %s", time.Since(start).Round(time.Millisecond))
	return nil
}

// RefreshAired updates the shows that have an episode that has aired since the
// last time it ran, since an episode airing changes what is next.
func (s *Store) RefreshAired() error {
	last := s.KVGet(upNextDayKey)
	now := today()
	if last == "" {
		last = time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	}
	if last >= now {
		return nil
	}
	rows, err := s.DB.Query(`SELECT DISTINCT show_id FROM episodes WHERE season_number > 0 AND air_date > ? AND air_date <= ?`, last, now)
	if err != nil {
		return fmt.Errorf("shows with newly aired episodes: %w", err)
	}
	var shows []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		shows = append(shows, id)
	}
	rows.Close()
	for _, id := range shows {
		s.refreshUpNextShow(id)
	}
	return s.KVSet(upNextDayKey, now)
}

// RunUpNextMaintenance keeps the table honest: new airings every hour and a
// full re-check once a day.
func (s *Store) RunUpNextMaintenance(ctx context.Context) {
	if err := s.RefreshAired(); err != nil {
		logger.Error("UPNEXT refresh aired: %v", err)
	}
	hourly := time.NewTicker(time.Hour)
	daily := time.NewTicker(24 * time.Hour)
	defer hourly.Stop()
	defer daily.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hourly.C:
			if err := s.EnsureUpNext(); err != nil {
				logger.Error("UPNEXT build: %v", err)
			}
			if err := s.RefreshAired(); err != nil {
				logger.Error("UPNEXT refresh aired: %v", err)
			}
		case <-daily.C:
			if err := s.RebuildUpNext(); err != nil {
				logger.Error("UPNEXT daily re-check: %v", err)
			}
		}
	}
}
