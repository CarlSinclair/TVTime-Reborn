package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// starBuckets folds (rating, count) rows into whole-star counts, index 0
// for one star up to index 4 for five.
func starBuckets(rows *sql.Rows) ([5]int, error) {
	var out [5]int
	defer rows.Close()
	for rows.Next() {
		var rating, n int
		if err := rows.Scan(&rating, &n); err != nil {
			return out, err
		}
		stars := (rating + 1) / 2
		if stars >= 1 && stars <= 5 {
			out[stars-1] += n
		}
	}
	return out, rows.Err()
}

func (s *Store) StarCounts(episodeID int64) ([5]int, error) {
	rows, err := s.DB.Query(`SELECT rating, COUNT(*) FROM (
	  SELECT rating FROM episode_ratings WHERE episode_id = ?
	  UNION ALL
	  SELECT rr.rating FROM remote_ratings rr JOIN episodes e ON e.id = ? JOIN shows sh ON sh.id = e.show_id
	    WHERE rr.kind = 'episode' AND rr.show_tmdb = sh.tmdb_id AND rr.season = e.season_number AND rr.episode = e.episode_number
	  ) GROUP BY rating`, episodeID, episodeID)
	if err != nil {
		return [5]int{}, fmt.Errorf("star counts episode=%d: %w", episodeID, err)
	}
	out, err := starBuckets(rows)
	if err != nil {
		return out, fmt.Errorf("star counts episode=%d: %w", episodeID, err)
	}
	return out, nil
}

func (s *Store) SeasonStarCounts(showID int64, season int) ([5]int, error) {
	rows, err := s.DB.Query(`SELECT rating, COUNT(*) FROM (
	  SELECT rating FROM season_ratings WHERE show_id = ? AND season_number = ?
	  UNION ALL
	  SELECT rr.rating FROM remote_ratings rr JOIN shows sh ON sh.id = ?
	    WHERE rr.kind = 'season' AND rr.show_tmdb = sh.tmdb_id AND rr.season = ?
	  ) GROUP BY rating`, showID, season, showID, season)
	if err != nil {
		return [5]int{}, fmt.Errorf("season star counts show=%d season=%d: %w", showID, season, err)
	}
	out, err := starBuckets(rows)
	if err != nil {
		return out, fmt.Errorf("season star counts show=%d season=%d: %w", showID, season, err)
	}
	return out, nil
}

func (s *Store) ShowStarCounts(showID int64) ([5]int, error) {
	rows, err := s.DB.Query(`SELECT rating, COUNT(*) FROM (
	  SELECT rating FROM show_ratings WHERE show_id = ?
	  UNION ALL
	  SELECT rr.rating FROM remote_ratings rr JOIN shows sh ON sh.id = ?
	    WHERE rr.kind = 'show' AND rr.show_tmdb = sh.tmdb_id
	  ) GROUP BY rating`, showID, showID)
	if err != nil {
		return [5]int{}, fmt.Errorf("show star counts show=%d: %w", showID, err)
	}
	out, err := starBuckets(rows)
	if err != nil {
		return out, fmt.Errorf("show star counts show=%d: %w", showID, err)
	}
	return out, nil
}

// RateSeason stores or (rating 0) clears a person's rating of a whole season.
func (s *Store) RateSeason(userID, showID int64, season, rating int) error {
	if rating == 0 {
		if _, err := s.DB.Exec(`DELETE FROM season_ratings WHERE user_id = ? AND show_id = ? AND season_number = ?`, userID, showID, season); err != nil {
			return fmt.Errorf("clear season rating uid=%d show=%d season=%d: %w", userID, showID, season, err)
		}
		return nil
	}
	if _, err := s.DB.Exec(`INSERT INTO season_ratings (user_id, show_id, season_number, rating, updated_at) VALUES (?,?,?,?,?)
	  ON CONFLICT(user_id, show_id, season_number) DO UPDATE SET rating = excluded.rating, updated_at = excluded.updated_at`, userID, showID, season, rating, Now()); err != nil {
		return fmt.Errorf("rate season uid=%d show=%d season=%d: %w", userID, showID, season, err)
	}
	return nil
}

// RateShow stores or (rating 0) clears a person's rating of a whole show.
func (s *Store) RateShow(userID, showID int64, rating int) error {
	if rating == 0 {
		if _, err := s.DB.Exec(`DELETE FROM show_ratings WHERE user_id = ? AND show_id = ?`, userID, showID); err != nil {
			return fmt.Errorf("clear show rating uid=%d show=%d: %w", userID, showID, err)
		}
		return nil
	}
	if _, err := s.DB.Exec(`INSERT INTO show_ratings (user_id, show_id, rating, updated_at) VALUES (?,?,?,?)
	  ON CONFLICT(user_id, show_id) DO UPDATE SET rating = excluded.rating, updated_at = excluded.updated_at`, userID, showID, rating, Now()); err != nil {
		return fmt.Errorf("rate show uid=%d show=%d: %w", userID, showID, err)
	}
	return nil
}

func (s *Store) UserSeasonRating(userID, showID int64, season int) (int, error) {
	var r int
	err := s.DB.QueryRow(`SELECT rating FROM season_ratings WHERE user_id = ? AND show_id = ? AND season_number = ?`, userID, showID, season).Scan(&r)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("season rating uid=%d show=%d season=%d: %w", userID, showID, season, err)
	}
	return r, nil
}

func (s *Store) UserShowRating(userID, showID int64) (int, error) {
	var r int
	err := s.DB.QueryRow(`SELECT rating FROM show_ratings WHERE user_id = ? AND show_id = ?`, userID, showID).Scan(&r)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("show rating uid=%d show=%d: %w", userID, showID, err)
	}
	return r, nil
}

// Rating is one rating as it travels between servers: of an episode, a season
// (Episode 0) or a whole show (Season and Episode 0), by TMDB show id, on the
// same 1 to 10 scale as the tables. Rating 0 takes a rating back.
type Rating struct {
	Kind     string
	ShowTMDB int64
	Season   int
	Episode  int
	Rating   int
}

// LocalRatings is every rating a local person has given, for sharing.
func (s *Store) LocalRatings(userID int64) ([]Rating, error) {
	rows, err := s.DB.Query(`SELECT 'episode', sh.tmdb_id, e.season_number, e.episode_number, r.rating
	  FROM episode_ratings r JOIN episodes e ON e.id = r.episode_id JOIN shows sh ON sh.id = e.show_id WHERE r.user_id = ?
	  UNION ALL
	  SELECT 'season', sh.tmdb_id, r.season_number, 0, r.rating FROM season_ratings r JOIN shows sh ON sh.id = r.show_id WHERE r.user_id = ?
	  UNION ALL
	  SELECT 'show', sh.tmdb_id, 0, 0, r.rating FROM show_ratings r JOIN shows sh ON sh.id = r.show_id WHERE r.user_id = ?`, userID, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("local ratings uid=%d: %w", userID, err)
	}
	defer rows.Close()
	var out []Rating
	for rows.Next() {
		var r Rating
		if err := rows.Scan(&r.Kind, &r.ShowTMDB, &r.Season, &r.Episode, &r.Rating); err != nil {
			return nil, fmt.Errorf("local ratings scan uid=%d: %w", userID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LocalUserIDs lists every person with an account on this server.
func (s *Store) LocalUserIDs() ([]int64, error) {
	rows, err := s.DB.Query(`SELECT id FROM users WHERE remote_host = '' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("local users: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("local users scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SaveRemoteRating keeps (or with Rating 0 removes) a rating given on another
// server by the shadow account userID. Remote ratings never expire.
func (s *Store) SaveRemoteRating(userID int64, r Rating, at string) error {
	if r.Rating == 0 {
		if _, err := s.DB.Exec(`DELETE FROM remote_ratings WHERE user_id = ? AND kind = ? AND show_tmdb = ? AND season = ? AND episode = ?`,
			userID, r.Kind, r.ShowTMDB, r.Season, r.Episode); err != nil {
			return fmt.Errorf("remove remote rating uid=%d %s show=%d: %w", userID, r.Kind, r.ShowTMDB, err)
		}
		return nil
	}
	if _, err := s.DB.Exec(`INSERT INTO remote_ratings (user_id, kind, show_tmdb, season, episode, rating, updated_at) VALUES (?,?,?,?,?,?,?)
	  ON CONFLICT(user_id, kind, show_tmdb, season, episode) DO UPDATE SET rating = excluded.rating, updated_at = excluded.updated_at`,
		userID, r.Kind, r.ShowTMDB, r.Season, r.Episode, r.Rating, at); err != nil {
		return fmt.Errorf("save remote rating uid=%d %s show=%d: %w", userID, r.Kind, r.ShowTMDB, err)
	}
	return nil
}

// DropRemoteRatingsFrom deletes every rating that came from a server, for when
// an admin decides its numbers cannot be trusted.
func (s *Store) DropRemoteRatingsFrom(host string) (int64, error) {
	res, err := s.DB.Exec(`DELETE FROM remote_ratings WHERE user_id IN (SELECT id FROM users WHERE remote_host = ?)`, strings.ToLower(host))
	if err != nil {
		return 0, fmt.Errorf("drop remote ratings from %s: %w", host, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeleteUserRatings erases every rating a person gave, of episodes, seasons and
// shows, including the copies kept for a person on another server.
func (s *Store) DeleteUserRatings(userID int64) error {
	for _, table := range []string{"episode_ratings", "season_ratings", "show_ratings", "remote_ratings"} {
		if _, err := s.DB.Exec(`DELETE FROM `+table+` WHERE user_id = ?`, userID); err != nil {
			return fmt.Errorf("delete %s of uid=%d: %w", table, userID, err)
		}
	}
	return nil
}

// AnonymizeRatings hands everything a person rated to a new anonymous account,
// so the ratings stay in the graphs when the person's own account is deleted.
// Each person gets their own placeholder because a rating is unique per rater.
func (s *Store) AnonymizeRatings(userID int64) error {
	tables := []string{"episode_ratings", "season_ratings", "show_ratings"}
	tx, err := s.DB.Begin()
	if err != nil {
		return fmt.Errorf("anonymize ratings uid=%d: %w", userID, err)
	}
	defer tx.Rollback()
	total := 0
	for _, table := range tables {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE user_id = ?`, userID).Scan(&n); err != nil {
			return fmt.Errorf("anonymize ratings uid=%d: count %s: %w", userID, table, err)
		}
		total += n
	}
	if total == 0 {
		return nil
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("anonymize ratings uid=%d: random name: %w", userID, err)
	}
	res, err := tx.Exec(`INSERT INTO users (username, display_name, is_deactivated, created_at) VALUES (?, 'Anonymous', 1, ?)`, "deleted_"+hex.EncodeToString(b), Now())
	if err != nil {
		return fmt.Errorf("anonymize ratings uid=%d: create placeholder: %w", userID, err)
	}
	anon, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("anonymize ratings uid=%d: %w", userID, err)
	}
	for _, table := range tables {
		if _, err := tx.Exec(`UPDATE `+table+` SET user_id = ? WHERE user_id = ?`, anon, userID); err != nil {
			return fmt.Errorf("anonymize ratings uid=%d: move %s: %w", userID, table, err)
		}
	}
	return tx.Commit()
}
