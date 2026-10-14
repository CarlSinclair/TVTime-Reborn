package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type Show struct {
	ID               int64
	TMDBID           int64
	TVDBID           int64
	IMDbID           string
	Name             string
	OriginalName     string
	Overview         string
	Status           string
	FirstAirDate     string
	LastAirDate      string
	PosterPath       string
	BackdropPath     string
	VoteAverage      float64
	VoteCount        int
	Genres           string
	Networks         string
	OriginalLanguage string
	NumberOfSeasons  int
	NumberOfEpisodes int
	Runtime          int
	NeedsSync        bool
	LastSyncedAt     string
	SyncError        string
}

type Season struct {
	ID           int64
	ShowID       int64
	SeasonNumber int
	Name         string
	Overview     string
	PosterPath   string
	AirDate      string
	EpisodeCount int
}

type Episode struct {
	ID            int64
	ShowID        int64
	TMDBID        int64
	SeasonNumber  int
	EpisodeNumber int
	Name          string
	Overview      string
	AirDate       string
	Runtime       int
	StillPath     string
	VoteAverage   float64
	VoteCount     int
	Watched       bool
	UserRating    int
	AirAt         string
}

const showCols = `id, tmdb_id, COALESCE(tvdb_id,0), COALESCE(imdb_id,''), name, original_name, overview, status,
  COALESCE(first_air_date,''), COALESCE(last_air_date,''), poster_path, backdrop_path, vote_average, vote_count,
  genres, networks, original_language, number_of_seasons, number_of_episodes, runtime, needs_sync, COALESCE(last_synced_at,''), sync_error`

func scanShow(row interface{ Scan(...any) error }) (*Show, error) {
	var sh Show
	var ns int
	if err := row.Scan(&sh.ID, &sh.TMDBID, &sh.TVDBID, &sh.IMDbID, &sh.Name, &sh.OriginalName, &sh.Overview, &sh.Status,
		&sh.FirstAirDate, &sh.LastAirDate, &sh.PosterPath, &sh.BackdropPath, &sh.VoteAverage, &sh.VoteCount,
		&sh.Genres, &sh.Networks, &sh.OriginalLanguage, &sh.NumberOfSeasons, &sh.NumberOfEpisodes, &sh.Runtime, &ns,
		&sh.LastSyncedAt, &sh.SyncError); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	sh.NeedsSync = ns == 1
	return &sh, nil
}

func (s *Store) ShowByID(id int64) (*Show, error) {
	return scanShow(s.DB.QueryRow(`SELECT `+showCols+` FROM shows WHERE id = ?`, id))
}

func (s *Store) ShowByTMDB(tmdbID int64) (*Show, error) {
	return scanShow(s.DB.QueryRow(`SELECT `+showCols+` FROM shows WHERE tmdb_id = ?`, tmdbID))
}

func (s *Store) ShowByTVDB(tvdbID int64) (*Show, error) {
	return scanShow(s.DB.QueryRow(`SELECT `+showCols+` FROM shows WHERE tvdb_id = ?`, tvdbID))
}

func (s *Store) ShowByIMDb(imdb string) (*Show, error) {
	return scanShow(s.DB.QueryRow(`SELECT `+showCols+` FROM shows WHERE imdb_id = ?`, imdb))
}

func (s *Store) ShowByName(name string) (*Show, error) {
	return scanShow(s.DB.QueryRow(`SELECT `+showCols+` FROM shows WHERE name = ? COLLATE NOCASE OR original_name = ? COLLATE NOCASE ORDER BY vote_count DESC LIMIT 1`, name, name))
}

func (s *Store) SearchLocal(q string, limit int) ([]Show, error) {
	rows, err := s.DB.Query(`SELECT `+showCols+` FROM shows WHERE name LIKE ? ESCAPE '\' ORDER BY vote_count DESC LIMIT ?`, "%"+escapeLike(q)+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("search local shows q=%q: %w", q, err)
	}
	defer rows.Close()
	var out []Show
	for rows.Next() {
		sh, err := scanShow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan local show: %w", err)
		}
		out = append(out, *sh)
	}
	return out, rows.Err()
}

func escapeLike(q string) string {
	out := make([]byte, 0, len(q))
	for i := 0; i < len(q); i++ {
		if q[i] == '%' || q[i] == '_' || q[i] == '\\' {
			out = append(out, '\\')
		}
		out = append(out, q[i])
	}
	return string(out)
}

func (s *Store) ShowsNeedingSync(limit int) ([]int64, error) {
	rows, err := s.DB.Query(`SELECT tmdb_id FROM shows WHERE needs_sync = 1 ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list shows needing sync: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) MarkNeedsSync(tmdbIDs []int64) (int, error) {
	n := 0
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin mark needs sync: %w", err)
	}
	defer tx.Rollback()
	for _, id := range tmdbIDs {
		res, err := tx.Exec(`UPDATE shows SET needs_sync = 1 WHERE tmdb_id = ? AND needs_sync = 0`, id)
		if err != nil {
			return 0, fmt.Errorf("mark needs sync tmdb=%d: %w", id, err)
		}
		c, _ := res.RowsAffected()
		n += int(c)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit mark needs sync: %w", err)
	}
	return n, nil
}

// MarkStaleFollowedForSync flags followed, still-running shows whose last sync
// is older than the cutoff. This backstops the TMDB changes feed.
func (s *Store) MarkStaleFollowedForSync(cutoff string) (int, error) {
	res, err := s.DB.Exec(`UPDATE shows SET needs_sync = 1 WHERE needs_sync = 0
	  AND status NOT IN ('Ended','Canceled')
	  AND (last_synced_at IS NULL OR last_synced_at < ?)
	  AND id IN (SELECT show_id FROM follows)`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("mark stale followed for sync: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) RecordSyncError(tmdbID int64, msg string) error {
	_, err := s.DB.Exec(`UPDATE shows SET sync_error = ?, needs_sync = 0, last_synced_at = ? WHERE tmdb_id = ?`, msg, Now(), tmdbID)
	if err != nil {
		return fmt.Errorf("record sync error tmdb=%d: %w", tmdbID, err)
	}
	return nil
}

func (s *Store) Seasons(showID int64) ([]Season, error) {
	rows, err := s.DB.Query(`SELECT id, show_id, season_number, name, overview, poster_path, COALESCE(air_date,''), episode_count
	  FROM seasons WHERE show_id = ? ORDER BY season_number`, showID)
	if err != nil {
		return nil, fmt.Errorf("list seasons show=%d: %w", showID, err)
	}
	defer rows.Close()
	var out []Season
	for rows.Next() {
		var se Season
		if err := rows.Scan(&se.ID, &se.ShowID, &se.SeasonNumber, &se.Name, &se.Overview, &se.PosterPath, &se.AirDate, &se.EpisodeCount); err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

const episodeCols = `e.id, e.show_id, e.tmdb_id, e.season_number, e.episode_number, e.name, e.overview, COALESCE(e.air_date,''),
  e.runtime, e.still_path, e.vote_average, e.vote_count`

func (s *Store) Episodes(showID int64, season int, userID int64) ([]Episode, error) {
	rows, err := s.DB.Query(`SELECT `+episodeCols+`, CASE WHEN w.episode_id IS NULL THEN 0 ELSE 1 END, COALESCE(r.rating,0)
	  FROM episodes e
	  LEFT JOIN watched w ON w.episode_id = e.id AND w.user_id = ?
	  LEFT JOIN episode_ratings r ON r.episode_id = e.id AND r.user_id = ?
	  WHERE e.show_id = ? AND e.season_number = ? ORDER BY e.episode_number`, userID, userID, showID, season)
	if err != nil {
		return nil, fmt.Errorf("list episodes show=%d season=%d: %w", showID, season, err)
	}
	defer rows.Close()
	var out []Episode
	for rows.Next() {
		var ep Episode
		var w int
		if err := rows.Scan(&ep.ID, &ep.ShowID, &ep.TMDBID, &ep.SeasonNumber, &ep.EpisodeNumber, &ep.Name, &ep.Overview, &ep.AirDate,
			&ep.Runtime, &ep.StillPath, &ep.VoteAverage, &ep.VoteCount, &w, &ep.UserRating); err != nil {
			return nil, err
		}
		ep.Watched = w == 1
		out = append(out, ep)
	}
	return out, rows.Err()
}

func (s *Store) Episode(id, userID int64) (*Episode, error) {
	var ep Episode
	var w int
	err := s.DB.QueryRow(`SELECT `+episodeCols+`, CASE WHEN w.episode_id IS NULL THEN 0 ELSE 1 END, COALESCE(r.rating,0), COALESCE(e.air_at,'')
	  FROM episodes e
	  LEFT JOIN watched w ON w.episode_id = e.id AND w.user_id = ?
	  LEFT JOIN episode_ratings r ON r.episode_id = e.id AND r.user_id = ?
	  WHERE e.id = ?`, userID, userID, id).Scan(&ep.ID, &ep.ShowID, &ep.TMDBID, &ep.SeasonNumber, &ep.EpisodeNumber, &ep.Name, &ep.Overview, &ep.AirDate,
		&ep.Runtime, &ep.StillPath, &ep.VoteAverage, &ep.VoteCount, &w, &ep.UserRating, &ep.AirAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	ep.Watched = w == 1
	return &ep, nil
}

func (s *Store) EpisodeByNumber(showID int64, season, number int) (*Episode, error) {
	var id int64
	if err := s.DB.QueryRow(`SELECT id FROM episodes WHERE show_id = ? AND season_number = ? AND episode_number = ?`, showID, season, number).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.Episode(id, 0)
}

type EpisodeInput struct {
	TMDBID        int64
	SeasonNumber  int
	EpisodeNumber int
	Name          string
	Overview      string
	AirDate       string
	Runtime       int
	StillPath     string
	VoteAverage   float64
	VoteCount     int
}

type SeasonInput struct {
	Number       int
	Name         string
	Overview     string
	PosterPath   string
	AirDate      string
	EpisodeCount int
	Episodes     []EpisodeInput
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (s *Store) SaveShow(sh *Show, seasons []SeasonInput) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin save show tmdb=%d: %w", sh.TMDBID, err)
	}
	defer tx.Rollback()

	var tvdb any
	if sh.TVDBID != 0 {
		tvdb = sh.TVDBID
	}
	if _, err := tx.Exec(`INSERT INTO shows (tmdb_id, tvdb_id, imdb_id, name, original_name, overview, status, first_air_date, last_air_date,
	    poster_path, backdrop_path, vote_average, vote_count, genres, networks, original_language, number_of_seasons, number_of_episodes,
	    runtime, needs_sync, last_synced_at, sync_error, created_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?, '', ?)
	  ON CONFLICT(tmdb_id) DO UPDATE SET tvdb_id=excluded.tvdb_id, imdb_id=excluded.imdb_id, name=excluded.name, original_name=excluded.original_name,
	    overview=excluded.overview, status=excluded.status, first_air_date=excluded.first_air_date, last_air_date=excluded.last_air_date,
	    poster_path=excluded.poster_path, backdrop_path=excluded.backdrop_path, vote_average=excluded.vote_average, vote_count=excluded.vote_count,
	    genres=excluded.genres, networks=excluded.networks, original_language=excluded.original_language, number_of_seasons=excluded.number_of_seasons,
	    number_of_episodes=excluded.number_of_episodes, runtime=excluded.runtime, needs_sync=0, last_synced_at=excluded.last_synced_at, sync_error=''`,
		sh.TMDBID, tvdb, nullable(sh.IMDbID), sh.Name, sh.OriginalName, sh.Overview, sh.Status, nullable(sh.FirstAirDate), nullable(sh.LastAirDate),
		sh.PosterPath, sh.BackdropPath, sh.VoteAverage, sh.VoteCount, sh.Genres, sh.Networks, sh.OriginalLanguage, sh.NumberOfSeasons, sh.NumberOfEpisodes,
		sh.Runtime, Now(), Now()); err != nil {
		return 0, fmt.Errorf("upsert show tmdb=%d: %w", sh.TMDBID, err)
	}
	var showID int64
	if err := tx.QueryRow(`SELECT id FROM shows WHERE tmdb_id = ?`, sh.TMDBID).Scan(&showID); err != nil {
		return 0, fmt.Errorf("read show id tmdb=%d: %w", sh.TMDBID, err)
	}
	for _, se := range seasons {
		if _, err := tx.Exec(`INSERT INTO seasons (show_id, season_number, name, overview, poster_path, air_date, episode_count)
		  VALUES (?,?,?,?,?,?,?)
		  ON CONFLICT(show_id, season_number) DO UPDATE SET name=excluded.name, overview=excluded.overview, poster_path=excluded.poster_path,
		    air_date=excluded.air_date, episode_count=excluded.episode_count`,
			showID, se.Number, se.Name, se.Overview, se.PosterPath, nullable(se.AirDate), se.EpisodeCount); err != nil {
			return 0, fmt.Errorf("upsert season show=%d season=%d: %w", showID, se.Number, err)
		}
		for _, ep := range se.Episodes {
			if _, err := tx.Exec(`INSERT INTO episodes (show_id, tmdb_id, season_number, episode_number, name, overview, air_date, runtime, still_path, vote_average, vote_count)
			  VALUES (?,?,?,?,?,?,?,?,?,?,?)
			  ON CONFLICT(show_id, season_number, episode_number) DO UPDATE SET tmdb_id=excluded.tmdb_id, name=excluded.name, overview=excluded.overview,
			    air_date=excluded.air_date, runtime=excluded.runtime, still_path=excluded.still_path, vote_average=excluded.vote_average, vote_count=excluded.vote_count`,
				showID, ep.TMDBID, ep.SeasonNumber, ep.EpisodeNumber, ep.Name, ep.Overview, nullable(ep.AirDate), ep.Runtime, ep.StillPath, ep.VoteAverage, ep.VoteCount); err != nil {
				return 0, fmt.Errorf("upsert episode show=%d s%02de%02d: %w", showID, ep.SeasonNumber, ep.EpisodeNumber, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit save show tmdb=%d: %w", sh.TMDBID, err)
	}
	s.refreshUpNextShow(showID)
	return showID, nil
}

func (s *Store) SyncStateGet(key string) string {
	var v string
	if err := s.DB.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, key).Scan(&v); err != nil {
		return ""
	}
	return v
}

func (s *Store) SyncStateSet(key, value string) error {
	if _, err := s.DB.Exec(`INSERT INTO sync_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return fmt.Errorf("set sync_state %s: %w", key, err)
	}
	return nil
}

func (s *Store) Adjacent(showID int64, season, episode int) (prev, next *Episode, err error) {
	find := func(cmp, order string) (*Episode, error) {
		var id int64
		qerr := s.DB.QueryRow(`SELECT id FROM episodes WHERE show_id = ? AND season_number > 0 AND
		  (season_number `+cmp+` ? OR (season_number = ? AND episode_number `+cmp+` ?))
		  ORDER BY season_number `+order+`, episode_number `+order+` LIMIT 1`, showID, season, season, episode).Scan(&id)
		if qerr != nil {
			if errors.Is(qerr, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, qerr
		}
		return s.Episode(id, 0)
	}
	if prev, err = find("<", "DESC"); err != nil {
		return nil, nil, fmt.Errorf("prev episode show=%d: %w", showID, err)
	}
	if next, err = find(">", "ASC"); err != nil {
		return nil, nil, fmt.Errorf("next episode show=%d: %w", showID, err)
	}
	return prev, next, nil
}
