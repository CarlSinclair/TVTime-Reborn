package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// YouTubeVideo is one upload read from a feed.
type YouTubeVideo struct {
	ID          string
	Title       string
	Description string
	Published   time.Time
}

// YouTubeShow is a followed show fed by a YouTube channel or playlist.
type YouTubeShow struct {
	ID     int64
	TMDBID int64
	Source string
	Name   string
}

// YouTubeHit is a channel or playlist the site has seen (in search or from a
// peer) together with the catalog id its show has or will get.
type YouTubeHit struct {
	Source   string
	ShowTMDB int64
	Name     string
	Overview string
	Image    string
}

const youtubeIDBase = int64(1) << 41

var (
	placeholderRe = regexp.MustCompile(`^episode\d+$`)
	numberHintRe  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:ep|episode|e)\.?\s*#?(\d{1,3})(?:[^0-9]|$)`)
)

func youtubeEpisodeID(videoID string) int64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(videoID); i++ {
		h ^= uint64(videoID[i])
		h *= 1099511628211
	}
	return -int64(h >> 2)
}

func titleKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func namesRelated(a, b string) bool {
	ka, kb := titleKey(a), titleKey(b)
	if len([]rune(ka)) < 4 || len([]rune(kb)) < 4 {
		return ka != "" && ka == kb
	}
	return strings.Contains(ka, kb) || strings.Contains(kb, ka)
}

type legacyEpisode struct {
	id          int64
	placeholder bool
}

// matchLegacy finds the legacy episode a video title refers to: the same title
// or, for a series (loose), a legacy title of at least five characters
// contained in the video title or the other way round, else the episode number
// named in the title ("Ep 3") against a placeholder called "Episode 3". A
// channel only gets near matches, where the shorter title is most of the
// longer, so a legacy episode called "Review" cannot swallow other videos.
func matchLegacy(legacy map[string]legacyEpisode, title string, loose bool) (string, bool) {
	tk := titleKey(title)
	if tk == "" {
		return "", false
	}
	if _, ok := legacy[tk]; ok {
		return tk, true
	}
	best := ""
	for k := range legacy {
		if len([]rune(k)) < 5 || placeholderRe.MatchString(k) {
			continue
		}
		short, long := len(k), len(tk)
		if short > long {
			short, long = long, short
		}
		if !loose && short*10 < long*6 {
			continue
		}
		if (strings.Contains(tk, k) || strings.Contains(k, tk)) && len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		return best, true
	}
	if !loose {
		return "", false
	}
	if m := numberHintRe.FindStringSubmatch(title); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			if _, ok := legacy["episode"+strconv.Itoa(n)]; ok {
				return "episode" + strconv.Itoa(n), true
			}
		}
	}
	return "", false
}

func (s *Store) loadLegacy(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, showID int64) (map[string]legacyEpisode, string, error) {
	rows, err := q.Query(`SELECT id, name, COALESCE(air_date,'') FROM episodes WHERE show_id = ? AND season_number > 0 AND yt_video_id IS NULL`, showID)
	if err != nil {
		return nil, "", fmt.Errorf("load legacy episodes show=%d: %w", showID, err)
	}
	defer rows.Close()
	legacy := map[string]legacyEpisode{}
	maxAir := ""
	for rows.Next() {
		var id int64
		var name, air string
		if err := rows.Scan(&id, &name, &air); err != nil {
			return nil, "", fmt.Errorf("scan legacy episode show=%d: %w", showID, err)
		}
		if k := titleKey(name); k != "" {
			legacy[k] = legacyEpisode{id: id, placeholder: placeholderRe.MatchString(k)}
		}
		if air > maxAir {
			maxAir = air
		}
	}
	return legacy, maxAir, rows.Err()
}

// YouTubeSourceOf is the channel or playlist behind a show, or "" when the show
// is not fed from YouTube.
func (s *Store) YouTubeSourceOf(tmdbID int64) (string, error) {
	var src sql.NullString
	err := s.DB.QueryRow(`SELECT yt_source FROM shows WHERE tmdb_id = ?`, tmdbID).Scan(&src)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("youtube source of show tmdb=%d: %w", tmdbID, err)
	}
	return src.String, nil
}

// ShowTMDBByYouTubeSource is the id of the show already fed by a source, or 0.
func (s *Store) ShowTMDBByYouTubeSource(source string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT tmdb_id FROM shows WHERE yt_source = ?`, source).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("show of youtube source %s: %w", source, err)
	}
	return id, nil
}

// SetYouTubeSource attaches a source to a show that has none.
func (s *Store) SetYouTubeSource(showID int64, source string) error {
	if _, err := s.DB.Exec(`UPDATE shows SET yt_source = ? WHERE id = ? AND yt_source IS NULL`, source, showID); err != nil {
		return fmt.Errorf("set youtube source %s on show=%d: %w", source, showID, err)
	}
	return nil
}

// RememberYouTubeSource records a source and returns the catalog id of its show.
// A show given in h.ShowTMDB (an existing one the source was matched to) wins;
// otherwise the id already recorded is kept, or a new one is allocated from the
// row number, so ids are unique on this server and carry no meaning elsewhere.
func (s *Store) RememberYouTubeSource(h YouTubeHit) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin remember youtube source %s: %w", h.Source, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO yt_sources (source, name, overview, image) VALUES (?,?,?,?)
	  ON CONFLICT(source) DO UPDATE SET name = excluded.name, overview = excluded.overview, image = excluded.image`,
		h.Source, h.Name, h.Overview, h.Image); err != nil {
		return 0, fmt.Errorf("remember youtube source %s: %w", h.Source, err)
	}
	var rowID, cur int64
	if err := tx.QueryRow(`SELECT id, show_tmdb FROM yt_sources WHERE source = ?`, h.Source).Scan(&rowID, &cur); err != nil {
		return 0, fmt.Errorf("read youtube source %s: %w", h.Source, err)
	}
	id := cur
	switch {
	case h.ShowTMDB != 0:
		id = h.ShowTMDB
	case cur == 0:
		id = -(youtubeIDBase + rowID)
	}
	if id != cur {
		if _, err := tx.Exec(`UPDATE yt_sources SET show_tmdb = ? WHERE id = ?`, id, rowID); err != nil {
			return 0, fmt.Errorf("set youtube source %s show id: %w", h.Source, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit remember youtube source %s: %w", h.Source, err)
	}
	return id, nil
}

// YouTubeHitFor is the remembered source whose show has the given catalog id.
func (s *Store) YouTubeHitFor(tmdbID int64) (*YouTubeHit, error) {
	var h YouTubeHit
	err := s.DB.QueryRow(`SELECT source, show_tmdb, name, overview, image FROM yt_sources WHERE show_tmdb = ?`, tmdbID).
		Scan(&h.Source, &h.ShowTMDB, &h.Name, &h.Overview, &h.Image)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("youtube hit for show tmdb=%d: %w", tmdbID, err)
	}
	return &h, nil
}

// FollowedYouTubeShows lists the source-fed shows somebody is following.
func (s *Store) FollowedYouTubeShows() ([]YouTubeShow, error) {
	rows, err := s.DB.Query(`SELECT sh.id, sh.tmdb_id, sh.yt_source, sh.name FROM shows sh
	  WHERE sh.yt_source IS NOT NULL AND EXISTS (SELECT 1 FROM follows f WHERE f.show_id = sh.id AND f.status = 'watching')
	  ORDER BY sh.id`)
	if err != nil {
		return nil, fmt.Errorf("followed youtube shows: %w", err)
	}
	defer rows.Close()
	var out []YouTubeShow
	for rows.Next() {
		var y YouTubeShow
		if err := rows.Scan(&y.ID, &y.TMDBID, &y.Source, &y.Name); err != nil {
			return nil, fmt.Errorf("scan followed youtube show: %w", err)
		}
		out = append(out, y)
	}
	return out, rows.Err()
}

// FollowedAdoptions lists followed shows without a source that a search
// matched to a channel or playlist.
func (s *Store) FollowedAdoptions() ([]YouTubeShow, error) {
	rows, err := s.DB.Query(`SELECT sh.id, sh.tmdb_id, ys.source, sh.name FROM shows sh
	  JOIN yt_sources ys ON ys.show_tmdb = sh.tmdb_id
	  WHERE sh.yt_source IS NULL AND NOT EXISTS (SELECT 1 FROM shows o WHERE o.yt_source = ys.source)
	    AND EXISTS (SELECT 1 FROM follows f WHERE f.show_id = sh.id AND f.status = 'watching')
	  ORDER BY sh.id`)
	if err != nil {
		return nil, fmt.Errorf("followed youtube adoptions: %w", err)
	}
	defer rows.Close()
	var out []YouTubeShow
	for rows.Next() {
		var y YouTubeShow
		if err := rows.Scan(&y.ID, &y.TMDBID, &y.Source, &y.Name); err != nil {
			return nil, fmt.Errorf("scan youtube adoption: %w", err)
		}
		out = append(out, y)
	}
	return out, rows.Err()
}

// LegacyShowMatch finds an existing show without a source that a feed really
// is: its name relates to the feed's and enough of the feed's titles match its
// episodes (at least 3, or at least a fifth of the feed). It returns 0 when
// none does.
func (s *Store) LegacyShowMatch(name string, titles []string, loose bool) (int64, error) {
	rows, err := s.DB.Query(`SELECT id, tmdb_id, name FROM shows WHERE tmdb_id < 0 AND yt_source IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("legacy show match %q: %w", name, err)
	}
	type cand struct {
		id, tmdb int64
	}
	var cands []cand
	for rows.Next() {
		var c cand
		var n string
		if err := rows.Scan(&c.id, &c.tmdb, &n); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan legacy show candidate: %w", err)
		}
		if namesRelated(n, name) {
			cands = append(cands, c)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, fmt.Errorf("legacy show candidates %q: %w", name, err)
	}
	var best int64
	bestHits := 0
	for _, c := range cands {
		legacy, _, err := s.loadLegacy(s.DB, c.id)
		if err != nil {
			return 0, err
		}
		hits := 0
		for _, t := range titles {
			if _, ok := matchLegacy(legacy, t, loose); ok {
				hits++
			}
		}
		if hits > bestHits && (hits >= 3 || hits*5 >= len(titles)) {
			best, bestHits = c.tmdb, hits
		}
	}
	return best, nil
}

// KnownYouTubeVideos is the set of video ids already attached to a show.
func (s *Store) KnownYouTubeVideos(showID int64) (map[string]bool, error) {
	rows, err := s.DB.Query(`SELECT yt_video_id FROM episodes WHERE show_id = ? AND yt_video_id IS NOT NULL`, showID)
	if err != nil {
		return nil, fmt.Errorf("known youtube videos show=%d: %w", showID, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan known youtube video show=%d: %w", showID, err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SaveYouTubeShow creates the show for a source. An existing show of that
// source is left exactly as it is.
func (s *Store) SaveYouTubeShow(sh *Show, source string) (int64, error) {
	if existing, err := s.ShowTMDBByYouTubeSource(source); err != nil {
		return 0, err
	} else if existing != 0 {
		var id int64
		if err := s.DB.QueryRow(`SELECT id FROM shows WHERE tmdb_id = ?`, existing).Scan(&id); err != nil {
			return 0, fmt.Errorf("read youtube show id tmdb=%d: %w", existing, err)
		}
		return id, nil
	}
	id, err := s.SaveShow(sh, nil)
	if err != nil {
		return 0, err
	}
	if err := s.SetYouTubeSource(id, source); err != nil {
		return 0, err
	}
	return id, nil
}

// ResolveYouTubeSource is the local catalog id of the show a peer's message is
// about. An existing show of the source wins; else a local show without a
// source that the peer's show id points at and whose name relates is taken
// over; else the source is remembered so opening it creates the show.
func (s *Store) ResolveYouTubeSource(source string, hintTMDB int64, name, image string) (int64, error) {
	if id, err := s.ShowTMDBByYouTubeSource(source); err != nil || id != 0 {
		return id, err
	}
	if hintTMDB < 0 {
		var id int64
		var n string
		err := s.DB.QueryRow(`SELECT id, name FROM shows WHERE tmdb_id = ? AND yt_source IS NULL`, hintTMDB).Scan(&id, &n)
		switch {
		case err == nil && namesRelated(n, name):
			if err := s.SetYouTubeSource(id, source); err != nil {
				return 0, err
			}
			return hintTMDB, nil
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return 0, fmt.Errorf("resolve youtube source %s hint tmdb=%d: %w", source, hintTMDB, err)
		}
	}
	return s.RememberYouTubeSource(YouTubeHit{Source: source, Name: name, Image: image})
}

// YouTubeRefs names a show and an episode in terms every server shares: the
// source and the video. Both are "" for anything not fed from YouTube.
func (s *Store) YouTubeRefs(tmdbID int64, season, episode int) (string, string, error) {
	var src, vid sql.NullString
	err := s.DB.QueryRow(`SELECT sh.yt_source, e.yt_video_id FROM shows sh
	  LEFT JOIN episodes e ON e.show_id = sh.id AND e.season_number = ? AND e.episode_number = ?
	  WHERE sh.tmdb_id = ?`, season, episode, tmdbID).Scan(&src, &vid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("youtube refs show tmdb=%d s%de%d: %w", tmdbID, season, episode, err)
	}
	return src.String, vid.String, nil
}

// YouTubeEpisode is this server's season and episode number of a video.
func (s *Store) YouTubeEpisode(tmdbID int64, video string) (int, int, bool, error) {
	var season, episode int
	err := s.DB.QueryRow(`SELECT e.season_number, e.episode_number FROM episodes e JOIN shows sh ON sh.id = e.show_id
	  WHERE sh.tmdb_id = ? AND e.yt_video_id = ?`, tmdbID, video).Scan(&season, &episode)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("youtube episode of video %s show tmdb=%d: %w", video, tmdbID, err)
	}
	return season, episode, true, nil
}

// ApplyYouTubeVideos files feed uploads under a show and returns how many
// became new episodes. An upload matching a legacy episode (one without a
// video id) is attached to it, giving it the real upload time and thumbnail; an
// unmatched upload older than the newest legacy episode is ignored; every other
// upload becomes a new episode numbered after the highest one of its season,
// the publish year for a channel and season 1 for a playlist.
func (s *Store) ApplyYouTubeVideos(showID int64, vids []YouTubeVideo, yearly bool) (int, error) {
	added, _, err := s.applyYouTubeVideos(showID, vids, yearly, false)
	return added, err
}

func (s *Store) applyYouTubeVideos(showID int64, vids []YouTubeVideo, yearly, keepOld bool) (int, []int, error) {
	sort.Slice(vids, func(i, j int) bool { return vids[i].Published.Before(vids[j].Published) })
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, nil, fmt.Errorf("begin youtube videos show=%d: %w", showID, err)
	}
	defer tx.Rollback()

	known := map[string]bool{}
	krows, err := tx.Query(`SELECT yt_video_id FROM episodes WHERE show_id = ? AND yt_video_id IS NOT NULL`, showID)
	if err != nil {
		return 0, nil, fmt.Errorf("youtube videos known show=%d: %w", showID, err)
	}
	for krows.Next() {
		var id string
		if err := krows.Scan(&id); err != nil {
			krows.Close()
			return 0, nil, fmt.Errorf("youtube videos scan known show=%d: %w", showID, err)
		}
		known[id] = true
	}
	err = krows.Err()
	krows.Close()
	if err != nil {
		return 0, nil, fmt.Errorf("youtube videos known show=%d: %w", showID, err)
	}
	legacy, legacyMax, err := s.loadLegacy(tx, showID)
	if err != nil {
		return 0, nil, err
	}

	added, attached := 0, 0
	touched := map[int]bool{}
	for _, v := range vids {
		if known[v.ID] {
			continue
		}
		pub := v.Published.UTC()
		airDate := pub.Format("2006-01-02")
		if key, ok := matchLegacy(legacy, v.Title, !yearly); ok {
			le := legacy[key]
			if _, err := tx.Exec(`UPDATE episodes SET yt_video_id = ?, air_at = ?, air_source = 'youtube', still_path = ?, air_date = ?,
			    name = CASE WHEN ? THEN ? ELSE name END WHERE id = ?`,
				v.ID, Ts(pub), "yt:"+v.ID, airDate, le.placeholder, v.Title, le.id); err != nil {
				return 0, nil, fmt.Errorf("attach youtube video %s to episode=%d show=%d: %w", v.ID, le.id, showID, err)
			}
			delete(legacy, key)
			known[v.ID] = true
			attached++
			continue
		}
		if !keepOld && legacyMax != "" && airDate < legacyMax {
			continue
		}
		season := 1
		if yearly {
			season = pub.Year()
		}
		var next int
		if err := tx.QueryRow(`SELECT COALESCE(MAX(episode_number), 0) + 1 FROM episodes WHERE show_id = ? AND season_number = ?`, showID, season).Scan(&next); err != nil {
			return 0, nil, fmt.Errorf("youtube videos next episode number show=%d season=%d: %w", showID, season, err)
		}
		desc := v.Description
		if r := []rune(desc); len(r) > 1000 {
			desc = string(r[:1000])
		}
		if _, err := tx.Exec(`INSERT INTO episodes (show_id, tmdb_id, season_number, episode_number, name, overview, air_date, air_at, air_source, still_path, yt_video_id)
		  VALUES (?,?,?,?,?,?,?,?, 'youtube', ?, ?)`,
			showID, youtubeEpisodeID(v.ID), season, next, v.Title, desc, airDate, Ts(pub), "yt:"+v.ID, v.ID); err != nil {
			return 0, nil, fmt.Errorf("insert youtube video %s show=%d s%de%d: %w", v.ID, showID, season, next, err)
		}
		known[v.ID] = true
		touched[season] = true
		added++
	}
	if err := finishYouTubeEpisodes(tx, showID, touched); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("commit youtube videos show=%d: %w", showID, err)
	}
	if !keepOld && (added > 0 || attached > 0) {
		s.refreshUpNextShow(showID)
	}
	seasons := make([]int, 0, len(touched))
	for season := range touched {
		seasons = append(seasons, season)
	}
	return added, seasons, nil
}

func finishYouTubeEpisodes(tx *sql.Tx, showID int64, touched map[int]bool) error {
	for season := range touched {
		if _, err := tx.Exec(`INSERT INTO seasons (show_id, season_number, name, episode_count) VALUES (?, ?, ?, 0)
		  ON CONFLICT(show_id, season_number) DO NOTHING`, showID, season, fmt.Sprintf("Season %d", season)); err != nil {
			return fmt.Errorf("youtube videos season row show=%d season=%d: %w", showID, season, err)
		}
		if _, err := tx.Exec(`UPDATE seasons SET episode_count = (SELECT COUNT(*) FROM episodes WHERE show_id = ? AND season_number = ?) WHERE show_id = ? AND season_number = ?`,
			showID, season, showID, season); err != nil {
			return fmt.Errorf("youtube videos season count show=%d season=%d: %w", showID, season, err)
		}
	}
	if _, err := tx.Exec(`UPDATE shows SET
	    number_of_episodes = (SELECT COUNT(*) FROM episodes WHERE show_id = ? AND season_number > 0),
	    number_of_seasons = (SELECT COUNT(DISTINCT season_number) FROM episodes WHERE show_id = ? AND season_number > 0),
	    last_air_date = (SELECT MAX(air_date) FROM episodes WHERE show_id = ? AND season_number > 0),
	    needs_sync = 0, last_synced_at = ?, sync_error = ''
	  WHERE id = ?`, showID, showID, showID, Now(), showID); err != nil {
		return fmt.Errorf("youtube videos update show=%d: %w", showID, err)
	}
	return nil
}

// BackfillYouTubePlaylist fills a playlist show's episode list with its whole
// playlist, oldest first, the first time the show is built. listed is in
// playlist order; a video the feed dated carries its real publish time, the
// others get an inferred date: older than the first dated one by the feed's
// typical gap, and never within the last three days, so nothing inferred can
// raise an alert. It returns how many episodes were added, and does nothing
// when the show already has episodes.
func (s *Store) BackfillYouTubePlaylist(showID int64, listed []YouTubeVideo) (int, error) {
	if len(listed) == 0 {
		return 0, nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin youtube backfill show=%d: %w", showID, err)
	}
	defer tx.Rollback()
	var have int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM episodes WHERE show_id = ? AND season_number > 0`, showID).Scan(&have); err != nil {
		return 0, fmt.Errorf("youtube backfill count show=%d: %w", showID, err)
	}
	if have > 0 {
		return 0, nil
	}
	dates := inferYouTubeDates(listed, time.Now().UTC().AddDate(0, 0, -3))
	for i, v := range listed {
		desc := v.Description
		if r := []rune(desc); len(r) > 1000 {
			desc = string(r[:1000])
		}
		src, at := "inferred", any(nil)
		if !v.Published.IsZero() {
			src, at = "youtube", Ts(v.Published.UTC())
		}
		if _, err := tx.Exec(`INSERT INTO episodes (show_id, tmdb_id, season_number, episode_number, name, overview, air_date, air_at, air_source, still_path, yt_video_id)
		  VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			showID, youtubeEpisodeID(v.ID), 1, i+1, v.Title, desc, dates[i].Format("2006-01-02"), at, src, "yt:"+v.ID, v.ID); err != nil {
			return 0, fmt.Errorf("youtube backfill insert video %s show=%d e%d: %w", v.ID, showID, i+1, err)
		}
	}
	if err := finishYouTubeEpisodes(tx, showID, map[int]bool{1: true}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit youtube backfill show=%d: %w", showID, err)
	}
	s.refreshUpNextShow(showID)
	return len(listed), nil
}

func inferYouTubeDates(listed []YouTubeVideo, latest time.Time) []time.Time {
	var known []time.Time
	first := -1
	for i, v := range listed {
		if !v.Published.IsZero() {
			known = append(known, v.Published.UTC())
			if first < 0 {
				first = i
			}
		}
	}
	step := 24 * time.Hour
	if len(known) > 1 {
		gaps := make([]time.Duration, 0, len(known)-1)
		for i := 1; i < len(known); i++ {
			g := known[i].Sub(known[i-1])
			if g < 0 {
				g = -g
			}
			gaps = append(gaps, g)
		}
		sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
		step = gaps[len(gaps)/2]
		if step < 24*time.Hour {
			step = 24 * time.Hour
		}
		if step > 14*24*time.Hour {
			step = 14 * 24 * time.Hour
		}
	}
	out := make([]time.Time, len(listed))
	prev := latest
	for i := range listed {
		switch {
		case !listed[i].Published.IsZero():
			out[i] = listed[i].Published.UTC()
		case first < 0:
			out[i] = latest.Add(-time.Duration(len(listed)-1-i) * step)
		case i < first:
			out[i] = listed[first].Published.UTC().Add(-time.Duration(first-i) * step)
		default:
			out[i] = prev
		}
		if out[i].After(latest) && listed[i].Published.IsZero() {
			out[i] = latest
		}
		prev = out[i]
	}
	return out
}

type YouTubeWatchTarget struct {
	EpisodeID int64
	ShowID    int64
	Followed  bool
}

// YouTubeWatchTargets finds the episode of each video id that this server has,
// and whether the person follows its show.
func (s *Store) YouTubeWatchTargets(userID int64, videoIDs []string) (map[string]YouTubeWatchTarget, error) {
	out := map[string]YouTubeWatchTarget{}
	const chunk = 500
	for start := 0; start < len(videoIDs); start += chunk {
		end := min(start+chunk, len(videoIDs))
		part := videoIDs[start:end]
		args := make([]any, 0, len(part)+1)
		args = append(args, userID)
		marks := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		for _, id := range part {
			args = append(args, id)
		}
		rows, err := s.DB.Query(`SELECT e.yt_video_id, e.id, e.show_id,
		    EXISTS (SELECT 1 FROM follows f WHERE f.user_id = ? AND f.show_id = e.show_id)
		  FROM episodes e WHERE e.yt_video_id IN (`+marks+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("youtube watch targets uid=%d: %w", userID, err)
		}
		for rows.Next() {
			var vid string
			var t YouTubeWatchTarget
			var followed int
			if err := rows.Scan(&vid, &t.EpisodeID, &t.ShowID, &followed); err != nil {
				rows.Close()
				return nil, fmt.Errorf("youtube watch targets uid=%d: %w", userID, err)
			}
			t.Followed = followed == 1
			out[vid] = t
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("youtube watch targets uid=%d: %w", userID, err)
		}
	}
	return out, nil
}

type FollowedChannel struct {
	ShowID int64
	Name   string
}

// FollowedYouTubeChannels maps each channel a person follows to the show that
// carries it.
func (s *Store) FollowedYouTubeChannels(userID int64) (map[string]FollowedChannel, error) {
	rows, err := s.DB.Query(`SELECT sh.yt_source, sh.id, sh.name FROM shows sh JOIN follows f ON f.show_id = sh.id
	  WHERE f.user_id = ? AND sh.yt_source LIKE 'UC%'`, userID)
	if err != nil {
		return nil, fmt.Errorf("followed youtube channels uid=%d: %w", userID, err)
	}
	defer rows.Close()
	out := map[string]FollowedChannel{}
	for rows.Next() {
		var src string
		var c FollowedChannel
		if err := rows.Scan(&src, &c.ShowID, &c.Name); err != nil {
			return nil, fmt.Errorf("scan followed youtube channel uid=%d: %w", userID, err)
		}
		out[src] = c
	}
	return out, rows.Err()
}

// BackfillYouTubeChannel files a channel's whole upload history, every video
// with its real publish time. Videos matching a legacy episode are attached to
// it; the others become episodes of their publish year's season, with no
// "older than the newest legacy episode" cutoff. A season whose episodes all
// have a video id is then renumbered by publish time, so the order is the
// channel's, wherever the videos arrived from. Watches follow episode ids, so
// renumbering never moves a watch.
func (s *Store) BackfillYouTubeChannel(showID int64, vids []YouTubeVideo) (int, error) {
	added, touched, err := s.applyYouTubeVideos(showID, vids, true, true)
	if err != nil {
		return 0, err
	}
	for _, season := range touched {
		if err := s.renumberYouTubeSeason(showID, season); err != nil {
			return added, err
		}
	}
	if added > 0 {
		s.refreshUpNextShow(showID)
	}
	return added, nil
}

func (s *Store) renumberYouTubeSeason(showID int64, season int) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin renumber show=%d season=%d: %w", showID, season, err)
	}
	defer tx.Rollback()
	var unlinked int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM episodes WHERE show_id = ? AND season_number = ? AND yt_video_id IS NULL`, showID, season).Scan(&unlinked); err != nil {
		return fmt.Errorf("renumber check show=%d season=%d: %w", showID, season, err)
	}
	if unlinked > 0 {
		return nil
	}
	rows, err := tx.Query(`SELECT id FROM episodes WHERE show_id = ? AND season_number = ? ORDER BY COALESCE(air_at, air_date), id`, showID, season)
	if err != nil {
		return fmt.Errorf("renumber list show=%d season=%d: %w", showID, season, err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("renumber scan show=%d season=%d: %w", showID, season, err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("renumber list show=%d season=%d: %w", showID, season, err)
	}
	if _, err := tx.Exec(`UPDATE episodes SET episode_number = -id WHERE show_id = ? AND season_number = ?`, showID, season); err != nil {
		return fmt.Errorf("renumber park show=%d season=%d: %w", showID, season, err)
	}
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE episodes SET episode_number = ? WHERE id = ?`, i+1, id); err != nil {
			return fmt.Errorf("renumber episode=%d show=%d season=%d: %w", id, showID, season, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit renumber show=%d season=%d: %w", showID, season, err)
	}
	return nil
}
