package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var validStatuses = map[string]bool{"watching": true, "plan": true, "completed": true, "dropped": true, "paused": true}

func ValidStatus(s string) bool { return validStatuses[s] }

func today() string { return time.Now().UTC().Format("2006-01-02") }

type Follow struct {
	Status   string
	Favorite bool
}

func (s *Store) FollowOf(userID, showID int64) (*Follow, error) {
	var f Follow
	var fav int
	err := s.DB.QueryRow(`SELECT status, favorite FROM follows WHERE user_id = ? AND show_id = ?`, userID, showID).Scan(&f.Status, &fav)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("follow lookup uid=%d show=%d: %w", userID, showID, err)
	}
	f.Favorite = fav == 1
	return &f, nil
}

func (s *Store) Follow(userID, showID int64, status string) error {
	if !ValidStatus(status) {
		status = "watching"
	}
	if _, err := s.DB.Exec(`INSERT INTO follows (user_id, show_id, status, created_at) VALUES (?,?,?,?)
	  ON CONFLICT(user_id, show_id) DO UPDATE SET status = excluded.status`, userID, showID, status, Now()); err != nil {
		return fmt.Errorf("follow uid=%d show=%d: %w", userID, showID, err)
	}
	return nil
}

func (s *Store) Unfollow(userID, showID int64) error {
	if _, err := s.DB.Exec(`DELETE FROM follows WHERE user_id = ? AND show_id = ?`, userID, showID); err != nil {
		return fmt.Errorf("unfollow uid=%d show=%d: %w", userID, showID, err)
	}
	return nil
}

func (s *Store) SetFavorite(userID, showID int64, fav bool) error {
	v := 0
	if fav {
		v = 1
	}
	res, err := s.DB.Exec(`UPDATE follows SET favorite = ? WHERE user_id = ? AND show_id = ?`, v, userID, showID)
	if err != nil {
		return fmt.Errorf("favorite uid=%d show=%d: %w", userID, showID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkWatched(userID, episodeID int64, at string) error {
	if at == "" {
		at = Now()
	}
	res, err := s.DB.Exec(`INSERT INTO watched (user_id, episode_id, show_id, watched_at)
	  SELECT ?, e.id, e.show_id, ? FROM episodes e WHERE e.id = ?
	  ON CONFLICT(user_id, episode_id) DO NOTHING`, userID, at, episodeID)
	if err != nil {
		return fmt.Errorf("mark watched uid=%d ep=%d: %w", userID, episodeID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := s.DB.QueryRow(`SELECT 1 FROM episodes WHERE id = ?`, episodeID).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	}
	return nil
}

func (s *Store) Unwatch(userID, episodeID int64) error {
	if _, err := s.DB.Exec(`DELETE FROM watched WHERE user_id = ? AND episode_id = ?`, userID, episodeID); err != nil {
		return fmt.Errorf("unwatch uid=%d ep=%d: %w", userID, episodeID, err)
	}
	return nil
}

// MarkRange marks every aired episode of a show up to and including the given
// episode as watched; season 0 (specials) is never auto-marked.
func (s *Store) MarkUpTo(userID, showID int64, season, episode int) (int, error) {
	res, err := s.DB.Exec(`INSERT INTO watched (user_id, episode_id, show_id, watched_at)
	  SELECT ?, e.id, e.show_id, ? FROM episodes e
	  WHERE e.show_id = ? AND e.season_number > 0 AND e.air_date IS NOT NULL AND e.air_date <= ?
	    AND (e.season_number < ? OR (e.season_number = ? AND e.episode_number <= ?))
	  ON CONFLICT(user_id, episode_id) DO NOTHING`, userID, Now(), showID, today(), season, season, episode)
	if err != nil {
		return 0, fmt.Errorf("mark up to uid=%d show=%d s%de%d: %w", userID, showID, season, episode, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) MarkSeason(userID, showID int64, season int, watched bool) (int, error) {
	if !watched {
		res, err := s.DB.Exec(`DELETE FROM watched WHERE user_id = ? AND show_id = ? AND episode_id IN
		  (SELECT id FROM episodes WHERE show_id = ? AND season_number = ?)`, userID, showID, showID, season)
		if err != nil {
			return 0, fmt.Errorf("unmark season uid=%d show=%d season=%d: %w", userID, showID, season, err)
		}
		n, _ := res.RowsAffected()
		return int(n), nil
	}
	res, err := s.DB.Exec(`INSERT INTO watched (user_id, episode_id, show_id, watched_at)
	  SELECT ?, e.id, e.show_id, ? FROM episodes e
	  WHERE e.show_id = ? AND e.season_number = ? AND e.air_date IS NOT NULL AND e.air_date <= ?
	  ON CONFLICT(user_id, episode_id) DO NOTHING`, userID, Now(), showID, season, today())
	if err != nil {
		return 0, fmt.Errorf("mark season uid=%d show=%d season=%d: %w", userID, showID, season, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) Rate(userID, episodeID int64, rating int) error {
	if rating == 0 {
		if _, err := s.DB.Exec(`DELETE FROM episode_ratings WHERE user_id = ? AND episode_id = ?`, userID, episodeID); err != nil {
			return fmt.Errorf("clear rating uid=%d ep=%d: %w", userID, episodeID, err)
		}
		return nil
	}
	res, err := s.DB.Exec(`INSERT INTO episode_ratings (user_id, episode_id, rating, updated_at)
	  SELECT ?, id, ?, ? FROM episodes WHERE id = ?
	  ON CONFLICT(user_id, episode_id) DO UPDATE SET rating = excluded.rating, updated_at = excluded.updated_at`, userID, rating, Now(), episodeID)
	if err != nil {
		return fmt.Errorf("rate uid=%d ep=%d: %w", userID, episodeID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type Progress struct {
	Watched int
	Aired   int
}

func (s *Store) Progress(userID, showID int64) (Progress, error) {
	var p Progress
	err := s.DB.QueryRow(`SELECT
	  (SELECT COUNT(*) FROM watched w JOIN episodes e ON e.id = w.episode_id WHERE w.user_id = ? AND w.show_id = ? AND e.season_number > 0),
	  (SELECT COUNT(*) FROM episodes WHERE show_id = ? AND season_number > 0 AND air_date IS NOT NULL AND air_date <= ?)`,
		userID, showID, showID, today()).Scan(&p.Watched, &p.Aired)
	if err != nil {
		return p, fmt.Errorf("progress uid=%d show=%d: %w", userID, showID, err)
	}
	return p, nil
}

// CompleteFinished moves shows from "watching" to "completed" once the show has
// ended and every aired regular episode is watched. userID or showID of 0 means
// all users or all shows. It returns how many follows changed.
func (s *Store) CompleteFinished(userID, showID int64) (int64, error) {
	res, err := s.DB.Exec(`UPDATE follows SET status = 'completed'
	  WHERE status = 'watching' AND (? = 0 OR user_id = ?) AND (? = 0 OR show_id = ?)
	    AND show_id IN (SELECT id FROM shows WHERE LOWER(status) IN ('ended', 'canceled', 'cancelled'))
	    AND EXISTS (SELECT 1 FROM episodes e WHERE e.show_id = follows.show_id AND e.season_number > 0 AND e.air_date IS NOT NULL AND e.air_date <= ?)
	    AND NOT EXISTS (SELECT 1 FROM episodes e WHERE e.show_id = follows.show_id AND e.season_number > 0 AND e.air_date IS NOT NULL AND e.air_date <= ?
	      AND NOT EXISTS (SELECT 1 FROM watched w WHERE w.user_id = follows.user_id AND w.episode_id = e.id))`,
		userID, userID, showID, showID, today(), today())
	if err != nil {
		return 0, fmt.Errorf("complete finished uid=%d show=%d: %w", userID, showID, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// SeasonProgress counts, per season number, the aired episodes and how many of
// the season's episodes the user has watched.
func (s *Store) SeasonProgress(userID, showID int64) (map[int]Progress, error) {
	rows, err := s.DB.Query(`SELECT e.season_number,
	  COALESCE(SUM(CASE WHEN e.air_date IS NOT NULL AND e.air_date <= ? THEN 1 ELSE 0 END), 0), COUNT(w.episode_id)
	  FROM episodes e LEFT JOIN watched w ON w.episode_id = e.id AND w.user_id = ?
	  WHERE e.show_id = ? GROUP BY e.season_number`, today(), userID, showID)
	if err != nil {
		return nil, fmt.Errorf("season progress uid=%d show=%d: %w", userID, showID, err)
	}
	defer rows.Close()
	out := map[int]Progress{}
	for rows.Next() {
		var n int
		var p Progress
		if err := rows.Scan(&n, &p.Aired, &p.Watched); err != nil {
			return nil, fmt.Errorf("season progress scan uid=%d show=%d: %w", userID, showID, err)
		}
		out[n] = p
	}
	return out, rows.Err()
}

func (p Progress) Done() bool { return p.Aired > 0 && p.Watched >= p.Aired }

// StatusCounts says how many shows someone has under each status.
func (s *Store) StatusCounts(userID int64) (map[string]int, error) {
	rows, err := s.DB.Query(`SELECT status, COUNT(*) FROM follows WHERE user_id = ? GROUP BY status`, userID)
	if err != nil {
		return nil, fmt.Errorf("status counts uid=%d: %w", userID, err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
		out["all"] += n
	}
	return out, rows.Err()
}

type ShowEntry struct {
	Show     Show
	Follow   Follow
	Progress Progress
}

func (s *Store) UserShows(userID int64, status string) ([]ShowEntry, error) {
	q := `SELECT shows.id, shows.tmdb_id, COALESCE(shows.tvdb_id,0), COALESCE(shows.imdb_id,''), shows.name, shows.original_name, shows.overview, shows.status,
	  COALESCE(shows.first_air_date,''), COALESCE(shows.last_air_date,''), shows.poster_path, shows.backdrop_path, shows.vote_average, shows.vote_count,
	  shows.genres, shows.networks, shows.original_language, shows.number_of_seasons, shows.number_of_episodes, shows.runtime, shows.needs_sync,
	  COALESCE(shows.last_synced_at,''), shows.sync_error, f.status, f.favorite
	  FROM follows f JOIN shows ON shows.id = f.show_id WHERE f.user_id = ?`
	args := []any{userID}
	if status != "" && status != "all" {
		q += ` AND f.status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY f.favorite DESC, shows.name COLLATE NOCASE`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list user shows uid=%d: %w", userID, err)
	}
	var out []ShowEntry
	for rows.Next() {
		var sh Show
		var ns, fav int
		var f Follow
		if err := rows.Scan(&sh.ID, &sh.TMDBID, &sh.TVDBID, &sh.IMDbID, &sh.Name, &sh.OriginalName, &sh.Overview, &sh.Status,
			&sh.FirstAirDate, &sh.LastAirDate, &sh.PosterPath, &sh.BackdropPath, &sh.VoteAverage, &sh.VoteCount,
			&sh.Genres, &sh.Networks, &sh.OriginalLanguage, &sh.NumberOfSeasons, &sh.NumberOfEpisodes, &sh.Runtime, &ns,
			&sh.LastSyncedAt, &sh.SyncError, &f.Status, &fav); err != nil {
			rows.Close()
			return nil, err
		}
		sh.NeedsSync = ns == 1
		f.Favorite = fav == 1
		out = append(out, ShowEntry{Show: sh, Follow: f})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		p, err := s.Progress(userID, out[i].Show.ID)
		if err != nil {
			return nil, err
		}
		out[i].Progress = p
	}
	return out, nil
}

type EpisodeRating struct {
	Season, Episode int
	TMDBAvg         float64
	TMDBVotes       int
	MemberAvg       float64
	MemberVotes     int
}

// ShowRatings lists every regular episode with TMDB's rating and the average of
// the ratings TVT members gave it (members rate in whole stars, stored x2).
func (s *Store) ShowRatings(showID int64) ([]EpisodeRating, error) {
	rows, err := s.DB.Query(`SELECT e.season_number, e.episode_number, e.vote_average, e.vote_count, COALESCE(AVG(r.rating),0), COUNT(r.rating)
	  FROM episodes e LEFT JOIN episode_ratings r ON r.episode_id = e.id
	  WHERE e.show_id = ? AND e.season_number > 0
	  GROUP BY e.id ORDER BY e.season_number, e.episode_number`, showID)
	if err != nil {
		return nil, fmt.Errorf("show ratings show=%d: %w", showID, err)
	}
	defer rows.Close()
	var out []EpisodeRating
	for rows.Next() {
		var r EpisodeRating
		if err := rows.Scan(&r.Season, &r.Episode, &r.TMDBAvg, &r.TMDBVotes, &r.MemberAvg, &r.MemberVotes); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type UpNextItem struct {
	Show      Show
	Episode   Episode
	Remaining int
}

func (s *Store) UpNext(userID int64, limit int) ([]UpNextItem, error) {
	return s.upNext(userID, 0, limit)
}

// UpNextFor is the card for one show: its first unwatched aired episode, or nil
// when the person is caught up (or not watching it).
func (s *Store) UpNextFor(userID, showID int64) (*UpNextItem, error) {
	list, err := s.upNext(userID, showID, 1)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func (s *Store) upNext(userID, showID int64, limit int) ([]UpNextItem, error) {
	rows, err := s.DB.Query(`SELECT s.id, s.tmdb_id, s.name, s.poster_path, s.backdrop_path,
	    `+episodeCols+`,
	    (SELECT COUNT(*) FROM episodes e3 WHERE e3.show_id = s.id AND e3.season_number > 0 AND e3.air_date IS NOT NULL AND e3.air_date <= ?
	      AND NOT EXISTS (SELECT 1 FROM watched w3 WHERE w3.user_id = f.user_id AND w3.episode_id = e3.id))
	  FROM follows f
	  JOIN shows s ON s.id = f.show_id
	  JOIN episodes e ON e.id = (
	    SELECT e2.id FROM episodes e2
	    WHERE e2.show_id = f.show_id AND e2.season_number > 0 AND e2.air_date IS NOT NULL AND e2.air_date <= ?
	      AND NOT EXISTS (SELECT 1 FROM watched w WHERE w.user_id = f.user_id AND w.episode_id = e2.id)
	    ORDER BY e2.season_number, e2.episode_number LIMIT 1)
	  WHERE f.user_id = ? AND f.status = 'watching' AND (? = 0 OR f.show_id = ?)
	  ORDER BY COALESCE((SELECT MAX(w4.watched_at) FROM watched w4 WHERE w4.user_id = f.user_id AND w4.show_id = s.id), f.created_at) DESC
	  LIMIT ?`, today(), today(), userID, showID, showID, limit)
	if err != nil {
		return nil, fmt.Errorf("up next uid=%d: %w", userID, err)
	}
	defer rows.Close()
	var out []UpNextItem
	for rows.Next() {
		var it UpNextItem
		if err := rows.Scan(&it.Show.ID, &it.Show.TMDBID, &it.Show.Name, &it.Show.PosterPath, &it.Show.BackdropPath,
			&it.Episode.ID, &it.Episode.ShowID, &it.Episode.TMDBID, &it.Episode.SeasonNumber, &it.Episode.EpisodeNumber, &it.Episode.Name,
			&it.Episode.Overview, &it.Episode.AirDate, &it.Episode.Runtime, &it.Episode.StillPath, &it.Episode.VoteAverage, &it.Episode.VoteCount,
			&it.Remaining); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

type CalendarItem struct {
	ShowID     int64
	ShowName   string
	PosterPath string
	Episode    Episode
}

func (s *Store) Calendar(userID int64, from, to string) ([]CalendarItem, error) {
	rows, err := s.DB.Query(`SELECT s.id, s.name, s.poster_path, `+episodeCols+`
	  FROM follows f JOIN shows s ON s.id = f.show_id JOIN episodes e ON e.show_id = s.id
	  WHERE f.user_id = ? AND f.status IN ('watching','plan') AND e.season_number > 0 AND e.air_date >= ? AND e.air_date <= ?
	  ORDER BY e.air_date, s.name COLLATE NOCASE, e.season_number, e.episode_number`, userID, from, to)
	if err != nil {
		return nil, fmt.Errorf("calendar uid=%d: %w", userID, err)
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

type Stats struct {
	Episodes int
	Minutes  int
	Shows    int
}

func (s *Store) UserStats(userID int64) (Stats, error) {
	var st Stats
	err := s.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN e.runtime > 0 THEN e.runtime ELSE sh.runtime END),0)
	  FROM watched w JOIN episodes e ON e.id = w.episode_id JOIN shows sh ON sh.id = w.show_id WHERE w.user_id = ?`, userID).Scan(&st.Episodes, &st.Minutes)
	if err != nil {
		return st, fmt.Errorf("stats uid=%d: %w", userID, err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM follows WHERE user_id = ?`, userID).Scan(&st.Shows); err != nil {
		return st, fmt.Errorf("stats shows uid=%d: %w", userID, err)
	}
	return st, nil
}

func (s *Store) ExportUser(userID int64) (map[string]any, error) {
	u, err := s.UserByID(userID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"username": u.Username, "email": u.Email, "display_name": u.DisplayName, "bio": u.Bio, "created_at": u.CreatedAt}

	rows, err := s.DB.Query(`SELECT sh.tmdb_id, sh.name, f.status, f.favorite FROM follows f JOIN shows sh ON sh.id = f.show_id WHERE f.user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("export follows uid=%d: %w", userID, err)
	}
	var follows []map[string]any
	for rows.Next() {
		var tmdb int64
		var name, status string
		var fav int
		if err := rows.Scan(&tmdb, &name, &status, &fav); err != nil {
			rows.Close()
			return nil, err
		}
		follows = append(follows, map[string]any{"tmdb_id": tmdb, "name": name, "status": status, "favorite": fav == 1})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export follows uid=%d: %w", userID, err)
	}
	out["follows"] = follows

	rows, err = s.DB.Query(`SELECT sh.tmdb_id, sh.name, e.season_number, e.episode_number, w.watched_at
	  FROM watched w JOIN episodes e ON e.id = w.episode_id JOIN shows sh ON sh.id = w.show_id WHERE w.user_id = ? ORDER BY w.watched_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("export watched uid=%d: %w", userID, err)
	}
	var watched []map[string]any
	for rows.Next() {
		var tmdb int64
		var name, at string
		var se, ep int
		if err := rows.Scan(&tmdb, &name, &se, &ep, &at); err != nil {
			rows.Close()
			return nil, err
		}
		watched = append(watched, map[string]any{"show_tmdb_id": tmdb, "show": name, "season": se, "episode": ep, "watched_at": at})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export watched uid=%d: %w", userID, err)
	}
	out["watched"] = watched

	rows, err = s.DB.Query(`SELECT sh.name, e.season_number, e.episode_number, c.body, c.created_at
	  FROM comments c JOIN episodes e ON e.id = c.episode_id JOIN shows sh ON sh.id = e.show_id WHERE c.user_id = ? AND c.deleted_at IS NULL ORDER BY c.created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("export comments uid=%d: %w", userID, err)
	}
	var comments []map[string]any
	for rows.Next() {
		var name, body, at string
		var se, ep int
		if err := rows.Scan(&name, &se, &ep, &body, &at); err != nil {
			rows.Close()
			return nil, err
		}
		comments = append(comments, map[string]any{"show": name, "season": se, "episode": ep, "body": body, "created_at": at})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export comments uid=%d: %w", userID, err)
	}
	out["comments"] = comments

	return out, nil
}
