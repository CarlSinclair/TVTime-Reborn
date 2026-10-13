package syncer

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"tvt/logger"
	"tvt/store"
	"tvt/tvdb"
)

// TVDBConfigured reports whether TheTVDB can be used as a second catalog.
func (s *Syncer) TVDBConfigured() bool { return s.TVDB.Configured() }

func tvdbStatus(name string) string {
	switch strings.ToLower(name) {
	case "continuing":
		return "Returning Series"
	case "upcoming":
		return "Planned"
	}
	return name
}

// SyncTVDBShow stores a show that TMDB does not have. It lives in the same
// tables as every other show with the negated TheTVDB id as its catalog id, so
// links, tracking and imports treat it like any other.
func (s *Syncer) SyncTVDBShow(ctx context.Context, tvdbID int64) (*store.Show, error) {
	if !s.TVDB.Configured() {
		return nil, tvdb.ErrNoKey
	}
	l := s.lockFor(fmt.Sprintf("show:%d", -tvdbID))
	l.Lock()
	defer l.Unlock()

	ser, err := s.TVDB.Series(ctx, tvdbID)
	if err != nil {
		return nil, fmt.Errorf("sync tvdb=%d series: %w", tvdbID, err)
	}
	eps, err := s.TVDB.Episodes(ctx, tvdbID)
	if err != nil {
		return nil, fmt.Errorf("sync tvdb=%d episodes: %w", tvdbID, err)
	}
	bySeason := map[int]*store.SeasonInput{}
	seen := map[[2]int]bool{}
	total := 0
	for _, e := range eps {
		if e.IsMovie == 1 {
			continue
		}
		key := [2]int{e.SeasonNumber, e.Number}
		if seen[key] {
			continue
		}
		seen[key] = true
		se, ok := bySeason[e.SeasonNumber]
		if !ok {
			name := fmt.Sprintf("Season %d", e.SeasonNumber)
			if e.SeasonNumber == 0 {
				name = "Specials"
			}
			se = &store.SeasonInput{Number: e.SeasonNumber, Name: name}
			bySeason[e.SeasonNumber] = se
		}
		still := ""
		if p := tvdb.ImagePath(e.Image); p != "" {
			still = "tvdb:" + p
		}
		se.Episodes = append(se.Episodes, store.EpisodeInput{
			TMDBID: -e.ID, SeasonNumber: e.SeasonNumber, EpisodeNumber: e.Number, Name: e.Name,
			Overview: strings.TrimSpace(e.Overview), AirDate: e.Aired, Runtime: e.Runtime, StillPath: still,
		})
		total++
	}
	numbers := make([]int, 0, len(bySeason))
	for n := range bySeason {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)
	inputs := make([]store.SeasonInput, 0, len(numbers))
	realSeasons := 0
	for _, n := range numbers {
		se := bySeason[n]
		se.EpisodeCount = len(se.Episodes)
		if n > 0 {
			realSeasons++
		}
		inputs = append(inputs, *se)
	}
	genres := make([]string, 0, len(ser.Genres))
	for _, g := range ser.Genres {
		genres = append(genres, g.Name)
	}
	poster := ""
	if p := tvdb.ImagePath(ser.Image); p != "" {
		poster = "tvdb:" + p
	}
	sh := &store.Show{
		TMDBID: -ser.ID, TVDBID: ser.ID, IMDbID: ser.IMDbID(), Name: ser.Name, OriginalName: ser.Name,
		Overview: strings.TrimSpace(ser.Overview), Status: tvdbStatus(ser.Status.Name),
		FirstAirDate: ser.FirstAired, LastAirDate: ser.LastAired, PosterPath: poster,
		Genres: strings.Join(genres, ", "), Networks: ser.OriginalNetwork.Name, OriginalLanguage: ser.OriginalLang,
		NumberOfSeasons: realSeasons, NumberOfEpisodes: total, Runtime: ser.AverageRuntime,
	}
	id, err := s.Store.SaveShow(sh, inputs)
	if err != nil {
		return nil, err
	}
	logger.Info("SYNC tvdb show tvdb=%d id=%d name=%q seasons=%d episodes=%d", tvdbID, id, ser.Name, len(inputs), total)
	return s.Store.ShowByID(id)
}
