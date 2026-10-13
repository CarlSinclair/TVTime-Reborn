package store

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type WatchStats struct {
	Lifetime int
	Today    int
	Week     int
	Month    int
	Episodes int
}

const episodeMinutes = `CASE WHEN e.runtime > 0 THEN e.runtime ELSE sh.runtime END`

// WatchStats reports minutes watched over four windows. "Today" starts at the
// user's local midnight; week and month are rolling 7 and 30 day windows.
func (s *Store) WatchStats(userID int64, loc *time.Location, now time.Time) (WatchStats, error) {
	var st WatchStats
	local := now.In(loc)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	bounds := [3]string{Ts(midnight), Ts(now.AddDate(0, 0, -7)), Ts(now.AddDate(0, 0, -30))}

	err := s.DB.QueryRow(`SELECT COUNT(*),
	    COALESCE(SUM(`+episodeMinutes+`),0),
	    COALESCE(SUM(CASE WHEN w.watched_at >= ? THEN `+episodeMinutes+` ELSE 0 END),0),
	    COALESCE(SUM(CASE WHEN w.watched_at >= ? THEN `+episodeMinutes+` ELSE 0 END),0),
	    COALESCE(SUM(CASE WHEN w.watched_at >= ? THEN `+episodeMinutes+` ELSE 0 END),0)
	  FROM watched w JOIN episodes e ON e.id = w.episode_id JOIN shows sh ON sh.id = w.show_id WHERE w.user_id = ?`,
		bounds[0], bounds[1], bounds[2], userID).Scan(&st.Episodes, &st.Lifetime, &st.Today, &st.Week, &st.Month)
	if err != nil {
		return st, fmt.Errorf("episode watch stats uid=%d: %w", userID, err)
	}
	return st, nil
}

type GenreStat struct {
	Name    string
	Minutes int
}

// FavoriteGenres ranks genres by minutes watched across shows.
func (s *Store) FavoriteGenres(userID int64, limit int) ([]GenreStat, error) {
	totals := map[string]int{}
	add := func(genres string, minutes int) {
		for _, g := range strings.Split(genres, ",") {
			g = strings.TrimSpace(g)
			if g != "" {
				totals[g] += minutes
			}
		}
	}
	rows, err := s.DB.Query(`SELECT sh.genres, COALESCE(SUM(`+episodeMinutes+`),0)
	  FROM watched w JOIN episodes e ON e.id = w.episode_id JOIN shows sh ON sh.id = w.show_id WHERE w.user_id = ? GROUP BY sh.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("favorite genres (shows) uid=%d: %w", userID, err)
	}
	for rows.Next() {
		var g string
		var m int
		if err := rows.Scan(&g, &m); err != nil {
			rows.Close()
			return nil, err
		}
		add(g, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("favorite genres (shows) uid=%d: %w", userID, err)
	}
	out := make([]GenreStat, 0, len(totals))
	for name, m := range totals {
		out = append(out, GenreStat{Name: name, Minutes: m})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Minutes != out[j].Minutes {
			return out[i].Minutes > out[j].Minutes
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) FavoriteShows(userID int64) ([]Show, error) {
	rows, err := s.DB.Query(`SELECT shows.id, shows.tmdb_id, COALESCE(shows.tvdb_id,0), COALESCE(shows.imdb_id,''), shows.name, shows.original_name, shows.overview, shows.status,
	  COALESCE(shows.first_air_date,''), COALESCE(shows.last_air_date,''), shows.poster_path, shows.backdrop_path, shows.vote_average, shows.vote_count,
	  shows.genres, shows.networks, shows.original_language, shows.number_of_seasons, shows.number_of_episodes, shows.runtime, shows.needs_sync,
	  COALESCE(shows.last_synced_at,''), shows.sync_error
	  FROM follows f JOIN shows ON shows.id = f.show_id WHERE f.user_id = ? AND f.favorite = 1 ORDER BY shows.name COLLATE NOCASE`, userID)
	if err != nil {
		return nil, fmt.Errorf("favorite shows uid=%d: %w", userID, err)
	}
	defer rows.Close()
	var out []Show
	for rows.Next() {
		sh, err := scanShow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sh)
	}
	return out, rows.Err()
}

func (s *Store) RecentlyWatched(userID int64, limit int) ([]CalendarItem, error) {
	rows, err := s.DB.Query(`SELECT sh.id, sh.name, sh.poster_path, `+episodeCols+`
	  FROM watched w JOIN episodes e ON e.id = w.episode_id JOIN shows sh ON sh.id = w.show_id
	  WHERE w.user_id = ? ORDER BY w.watched_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("recently watched uid=%d: %w", userID, err)
	}
	defer rows.Close()
	var out []CalendarItem
	for rows.Next() {
		var it CalendarItem
		if err := rows.Scan(&it.ShowID, &it.ShowName, &it.PosterPath,
			&it.Episode.ID, &it.Episode.ShowID, &it.Episode.TMDBID, &it.Episode.SeasonNumber, &it.Episode.EpisodeNumber, &it.Episode.Name,
			&it.Episode.Overview, &it.Episode.AirDate, &it.Episode.Runtime, &it.Episode.StillPath, &it.Episode.VoteAverage, &it.Episode.VoteCount); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
