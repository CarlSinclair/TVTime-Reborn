package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type UnmatchedShow struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type ImportJob struct {
	ID              int64
	UserID          int64
	Source          string
	Status          string
	Total           int
	Matched         int
	ShowsDone       int
	EpisodesAdded   int
	EpisodesSkipped int
	FollowsAdded    int
	Unmatched       []UnmatchedShow
	Error           string
	CreatedAt       string
	FinishedAt      string
}

func (j *ImportJob) Active() bool { return j.Status == "pending" || j.Status == "running" }

const importCols = `id, user_id, source, status, total, matched, shows_done, episodes_added, episodes_skipped, follows_added, unmatched, error, created_at, COALESCE(finished_at,'')`

func scanImport(sc interface{ Scan(...any) error }) (*ImportJob, error) {
	var j ImportJob
	var un string
	if err := sc.Scan(&j.ID, &j.UserID, &j.Source, &j.Status, &j.Total, &j.Matched, &j.ShowsDone, &j.EpisodesAdded, &j.EpisodesSkipped, &j.FollowsAdded, &un, &j.Error, &j.CreatedAt, &j.FinishedAt); err != nil {
		return nil, err
	}
	if un != "" {
		if err := json.Unmarshal([]byte(un), &j.Unmatched); err != nil {
			return nil, fmt.Errorf("import job %d unmatched list: %w", j.ID, err)
		}
	}
	return &j, nil
}

func (s *Store) CreateImportJob(userID int64, source, payload string, total int) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin import job uid=%d: %w", userID, err)
	}
	defer tx.Rollback()
	var busy int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM import_jobs WHERE user_id = ? AND status IN ('pending','running')`, userID).Scan(&busy); err != nil {
		return 0, fmt.Errorf("check active import uid=%d: %w", userID, err)
	}
	if busy > 0 {
		return 0, ErrImportBusy
	}
	res, err := tx.Exec(`INSERT INTO import_jobs (user_id, source, status, total, payload, created_at) VALUES (?,?,?,?,?,?)`, userID, source, "pending", total, payload, Now())
	if err != nil {
		return 0, fmt.Errorf("create import job uid=%d: %w", userID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

var ErrImportBusy = errors.New("an import is already running")

func (s *Store) LatestImport(userID int64) (*ImportJob, error) {
	j, err := scanImport(s.DB.QueryRow(`SELECT `+importCols+` FROM import_jobs WHERE user_id = ? ORDER BY id DESC LIMIT 1`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("latest import uid=%d: %w", userID, err)
	}
	return j, nil
}

func (s *Store) ImportJobWithPayload(id int64) (*ImportJob, string, error) {
	row := s.DB.QueryRow(`SELECT `+importCols+`, payload FROM import_jobs WHERE id = ?`, id)
	var j ImportJob
	var un, payload string
	if err := row.Scan(&j.ID, &j.UserID, &j.Source, &j.Status, &j.Total, &j.Matched, &j.ShowsDone, &j.EpisodesAdded, &j.EpisodesSkipped, &j.FollowsAdded, &un, &j.Error, &j.CreatedAt, &j.FinishedAt, &payload); err != nil {
		return nil, "", fmt.Errorf("import job %d: %w", id, err)
	}
	return &j, payload, nil
}

func (s *Store) StartImport(id int64) error {
	_, err := s.DB.Exec(`UPDATE import_jobs SET status = 'running' WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("start import %d: %w", id, err)
	}
	return nil
}

func (s *Store) SaveImportProgress(j *ImportJob) error {
	un, err := json.Marshal(j.Unmatched)
	if err != nil {
		return fmt.Errorf("encode unmatched import %d: %w", j.ID, err)
	}
	if len(j.Unmatched) == 0 {
		un = nil
	}
	if _, err := s.DB.Exec(`UPDATE import_jobs SET matched = ?, shows_done = ?, episodes_added = ?, episodes_skipped = ?, follows_added = ?, unmatched = ? WHERE id = ?`,
		j.Matched, j.ShowsDone, j.EpisodesAdded, j.EpisodesSkipped, j.FollowsAdded, string(un), j.ID); err != nil {
		return fmt.Errorf("save import progress %d: %w", j.ID, err)
	}
	return nil
}

func (s *Store) FinishImport(id int64, status, errMsg string) error {
	if _, err := s.DB.Exec(`UPDATE import_jobs SET status = ?, error = ?, payload = '', finished_at = ? WHERE id = ?`, status, errMsg, Now(), id); err != nil {
		return fmt.Errorf("finish import %d: %w", id, err)
	}
	return nil
}

func (s *Store) FailInterruptedImports() (int64, error) {
	res, err := s.DB.Exec(`UPDATE import_jobs SET status = 'failed', error = 'interrupted by a server restart', payload = '', finished_at = ? WHERE status IN ('pending','running')`, Now())
	if err != nil {
		return 0, fmt.Errorf("fail interrupted imports: %w", err)
	}
	return res.RowsAffected()
}

func (s *Store) PruneImports(olderThan time.Duration) error {
	_, err := s.DB.Exec(`DELETE FROM import_jobs WHERE status IN ('done','failed') AND created_at < ?`, Ts(time.Now().Add(-olderThan)))
	if err != nil {
		return fmt.Errorf("prune imports: %w", err)
	}
	return nil
}

func (s *Store) EpisodeIndex(showID int64) (map[[2]int]int64, error) {
	rows, err := s.DB.Query(`SELECT season_number, episode_number, id FROM episodes WHERE show_id = ?`, showID)
	if err != nil {
		return nil, fmt.Errorf("episode index show=%d: %w", showID, err)
	}
	defer rows.Close()
	out := map[[2]int]int64{}
	for rows.Next() {
		var se, ep int
		var id int64
		if err := rows.Scan(&se, &ep, &id); err != nil {
			return nil, err
		}
		out[[2]int{se, ep}] = id
	}
	return out, rows.Err()
}

type ImportedEpisode struct {
	EpisodeID int64
	At        string
}

type ImportShowInput struct {
	UserID, ShowID  int64
	Status          string
	CompletedIfDone bool
	Favorite        bool
	FollowedAt      string
	Episodes        []ImportedEpisode
}

type ImportShowResult struct {
	EpisodesAdded int
	FollowAdded   bool
}

// ImportShow adds history and a library entry without ever overwriting what the
// user already tracks: existing watches, status and follow date are kept, a
// favourite flag is only ever turned on.
func (s *Store) ImportShow(in ImportShowInput) (ImportShowResult, error) {
	var res ImportShowResult
	tx, err := s.DB.Begin()
	if err != nil {
		return res, fmt.Errorf("begin import show uid=%d show=%d: %w", in.UserID, in.ShowID, err)
	}
	defer tx.Rollback()
	for _, e := range in.Episodes {
		at := e.At
		if at == "" {
			at = Now()
		}
		r, err := tx.Exec(`INSERT OR IGNORE INTO watched (user_id, episode_id, show_id, watched_at) VALUES (?,?,?,?)`, in.UserID, e.EpisodeID, in.ShowID, at)
		if err != nil {
			return res, fmt.Errorf("import watch uid=%d episode=%d: %w", in.UserID, e.EpisodeID, err)
		}
		if n, _ := r.RowsAffected(); n > 0 {
			res.EpisodesAdded++
		}
	}
	if in.Status != "" {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM follows WHERE user_id = ? AND show_id = ?`, in.UserID, in.ShowID).Scan(&exists); err != nil {
			return res, fmt.Errorf("check follow uid=%d show=%d: %w", in.UserID, in.ShowID, err)
		}
		status := in.Status
		if exists == 0 && in.CompletedIfDone {
			var watched, aired int
			if err := tx.QueryRow(`SELECT
			  (SELECT COUNT(*) FROM watched w JOIN episodes e ON e.id = w.episode_id WHERE w.user_id = ? AND w.show_id = ? AND e.season_number > 0),
			  (SELECT COUNT(*) FROM episodes WHERE show_id = ? AND season_number > 0 AND air_date IS NOT NULL AND air_date <= ?)`,
				in.UserID, in.ShowID, in.ShowID, today()).Scan(&watched, &aired); err != nil {
				return res, fmt.Errorf("progress for import uid=%d show=%d: %w", in.UserID, in.ShowID, err)
			}
			if aired > 0 && watched >= aired {
				status = "completed"
			}
		}
		fav := 0
		if in.Favorite {
			fav = 1
		}
		when := in.FollowedAt
		if when == "" {
			when = Now()
		}
		if _, err := tx.Exec(`INSERT INTO follows (user_id, show_id, status, favorite, created_at) VALUES (?,?,?,?,?)
		  ON CONFLICT(user_id, show_id) DO UPDATE SET favorite = MAX(favorite, excluded.favorite)`, in.UserID, in.ShowID, status, fav, when); err != nil {
			return res, fmt.Errorf("import follow uid=%d show=%d: %w", in.UserID, in.ShowID, err)
		}
		res.FollowAdded = exists == 0
	}
	return res, tx.Commit()
}
