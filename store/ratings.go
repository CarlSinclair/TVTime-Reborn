package store

import "fmt"

// StarCounts is how many members gave an episode each whole-star rating, index 0
// for one star up to index 4 for five.
func (s *Store) StarCounts(episodeID int64) ([5]int, error) {
	var out [5]int
	rows, err := s.DB.Query(`SELECT rating, COUNT(*) FROM episode_ratings WHERE episode_id = ? GROUP BY rating`, episodeID)
	if err != nil {
		return out, fmt.Errorf("star counts episode=%d: %w", episodeID, err)
	}
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
