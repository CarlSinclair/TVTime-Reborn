package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"tvt/logger"
	"tvt/store"
	"tvt/youtube"
)

const (
	youtubePollEvery  = 5 * time.Minute
	youtubePause      = 300 * time.Millisecond
	youtubeShortCache = 5000
	youtubeFeedTTL    = 10 * time.Minute
	youtubeFeedCache  = 500
)

type cachedFeed struct {
	feed *youtube.Feed
	at   time.Time
}

func (s *Syncer) YouTubeConfigured() bool { return s.YouTube != nil }

// YouTubeResult is a search result that has been checked against the real feed
// of its channel or playlist.
type YouTubeResult struct {
	ID       int64
	Name     string
	Overview string
	Poster   string
}

func keyOf(v string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(v) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Syncer) readFeed(ctx context.Context, source string) (*youtube.Feed, error) {
	s.feedMu.Lock()
	if c, ok := s.feeds[source]; ok && time.Since(c.at) < youtubeFeedTTL {
		s.feedMu.Unlock()
		return c.feed, nil
	}
	s.feedMu.Unlock()
	feed, err := s.YouTube.Feed(ctx, source)
	if err != nil {
		return nil, err
	}
	s.feedMu.Lock()
	if len(s.feeds) >= youtubeFeedCache {
		s.feeds = map[string]cachedFeed{}
	}
	s.feeds[source] = cachedFeed{feed: feed, at: time.Now()}
	s.feedMu.Unlock()
	return feed, nil
}

// CheckYouTubeSource verifies a search result and gives it a catalog id. The
// result must have a readable feed with uploads whose title agrees with the
// result's, so what is listed is what following it would really deliver. The id
// is that of the show already following the source, else that of an existing
// show without a source that the feed's titles match, else a new local one.
func (s *Syncer) CheckYouTubeSource(ctx context.Context, src youtube.Source) (*YouTubeResult, error) {
	if s.YouTube == nil {
		return nil, errors.New("youtube: not configured")
	}
	feed, err := s.readFeed(ctx, src.ID)
	if err != nil {
		return nil, fmt.Errorf("youtube result %s %q: %w", src.ID, src.Title, err)
	}
	if len(feed.Videos) == 0 {
		return nil, fmt.Errorf("youtube result %s %q: the feed has no uploads", src.ID, src.Title)
	}
	fk, tk := keyOf(feed.Title), keyOf(src.Title)
	if fk == "" || tk == "" || !(strings.Contains(fk, tk) || strings.Contains(tk, fk)) {
		return nil, fmt.Errorf("youtube result %s %q: the feed is called %q", src.ID, src.Title, feed.Title)
	}
	newest := feed.Videos[0]
	titles := make([]string, 0, len(feed.Videos))
	for _, v := range feed.Videos {
		titles = append(titles, v.Title)
		if v.Published.After(newest.Published) {
			newest = v
		}
	}
	poster := "yt:" + newest.ID
	if !src.Playlist() {
		if p := youtube.AvatarPath(src.Avatar); p != "" {
			poster = p
		}
	}
	overview := src.Description
	if src.Playlist() && src.Owner != "" {
		overview = strings.TrimSpace(src.Owner + ": " + src.Description)
		overview = strings.TrimSuffix(overview, ":")
	}
	res := &YouTubeResult{Name: src.Title, Overview: overview, Poster: poster}
	if id, err := s.Store.ShowTMDBByYouTubeSource(src.ID); err != nil {
		return nil, err
	} else if id != 0 {
		res.ID = id
		return res, nil
	}
	adopt, err := s.Store.LegacyShowMatch(src.Title, titles, src.Playlist())
	if err != nil {
		logger.Error("SYNC youtube match %s %q to existing shows: %v", src.ID, src.Title, err)
	}
	res.ID, err = s.Store.RememberYouTubeSource(store.YouTubeHit{Source: src.ID, ShowTMDB: adopt, Name: src.Title, Overview: overview, Image: poster})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// SyncYouTubeShow brings a source-fed show up to date from its feed, creating
// the show first when it was only seen in search. The second result is false
// when the id is not a YouTube show at all.
func (s *Syncer) SyncYouTubeShow(ctx context.Context, tmdbID int64) (*store.Show, bool, error) {
	if s.YouTube == nil {
		return nil, false, errors.New("youtube sync: not configured")
	}
	l := s.lockFor(fmt.Sprintf("show:%d", tmdbID))
	l.Lock()
	defer l.Unlock()

	source, err := s.Store.YouTubeSourceOf(tmdbID)
	if err != nil {
		return nil, false, err
	}
	var hit *store.YouTubeHit
	if source == "" {
		if hit, err = s.Store.YouTubeHitFor(tmdbID); err != nil {
			return nil, false, err
		}
		if hit == nil {
			return nil, false, nil
		}
		source = hit.Source
	}
	feed, err := s.YouTube.Feed(ctx, source)
	if err != nil {
		return nil, true, fmt.Errorf("youtube sync tmdb=%d source=%s: %w", tmdbID, source, err)
	}
	var showID int64
	if hit != nil {
		existing, err := s.Store.ShowByTMDB(tmdbID)
		switch {
		case err == nil:
			showID = existing.ID
			if err := s.Store.SetYouTubeSource(showID, source); err != nil {
				return nil, true, err
			}
		case errors.Is(err, store.ErrNotFound):
			name := hit.Name
			if name == "" {
				name = feed.Title
			}
			showID, err = s.Store.SaveYouTubeShow(&store.Show{
				TMDBID: tmdbID, Name: name, OriginalName: name, Overview: hit.Overview, Status: "Returning Series",
				Networks: "YouTube", PosterPath: hit.Image,
			}, source)
			if err != nil {
				return nil, true, err
			}
		default:
			return nil, true, fmt.Errorf("youtube sync load show tmdb=%d: %w", tmdbID, err)
		}
	} else {
		sh, err := s.Store.ShowByTMDB(tmdbID)
		if err != nil {
			return nil, true, fmt.Errorf("youtube sync load show tmdb=%d: %w", tmdbID, err)
		}
		showID = sh.ID
	}
	if youtube.IsPlaylist(source) {
		if err := s.backfillPlaylist(ctx, tmdbID, showID, source, feed); err != nil {
			return nil, true, err
		}
	}
	known, err := s.Store.KnownYouTubeVideos(showID)
	if err != nil {
		return nil, true, err
	}
	var vids []store.YouTubeVideo
	for _, v := range feed.Videos {
		if known[v.ID] {
			continue
		}
		short, err := s.isShort(ctx, v.ID)
		if err != nil {
			logger.Warn("SYNC youtube tmdb=%d video=%s short check failed, retrying next poll: %v", tmdbID, v.ID, err)
			continue
		}
		if short {
			continue
		}
		vids = append(vids, store.YouTubeVideo{ID: v.ID, Title: v.Title, Description: v.Description, Published: v.Published})
	}
	added, err := s.Store.ApplyYouTubeVideos(showID, vids, !youtube.IsPlaylist(source))
	if err != nil {
		return nil, true, err
	}
	if added > 0 {
		logger.Info("SYNC youtube show tmdb=%d source=%s new_episodes=%d", tmdbID, source, added)
	}
	if !youtube.IsPlaylist(source) {
		s.startChannelBackfill(showID, tmdbID, source)
	}
	sh, err := s.Store.ShowByTMDB(tmdbID)
	if err != nil {
		return nil, true, fmt.Errorf("youtube sync reload show tmdb=%d: %w", tmdbID, err)
	}
	return sh, true, nil
}

// backfillPlaylist lists a new playlist show's whole playlist, not only the
// feed's latest entries, in the order the owner set (turned oldest first using
// the feed's publish times). The feed's videos keep their real times.
func (s *Syncer) backfillPlaylist(ctx context.Context, tmdbID, showID int64, source string, feed *youtube.Feed) error {
	known, err := s.Store.KnownYouTubeVideos(showID)
	if err != nil {
		return err
	}
	if len(known) > 0 {
		return nil
	}
	entries, err := s.YouTube.PlaylistVideos(ctx, source, 0)
	if err != nil {
		return fmt.Errorf("youtube sync tmdb=%d source=%s playlist listing: %w", tmdbID, source, err)
	}
	dated := map[string]youtube.Video{}
	for _, v := range feed.Videos {
		dated[v.ID] = v
	}
	var firstPos, lastPos = -1, -1
	for i, e := range entries {
		if _, ok := dated[e.ID]; ok {
			if firstPos < 0 {
				firstPos = i
			}
			lastPos = i
		}
	}
	if firstPos >= 0 && firstPos != lastPos && dated[entries[firstPos].ID].Published.After(dated[entries[lastPos].ID].Published) {
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	}
	listed := make([]store.YouTubeVideo, 0, len(entries))
	for _, e := range entries {
		v := store.YouTubeVideo{ID: e.ID, Title: e.Title}
		if f, ok := dated[e.ID]; ok {
			short, err := s.isShort(ctx, e.ID)
			if err != nil {
				return fmt.Errorf("youtube sync tmdb=%d video=%s short check: %w", tmdbID, e.ID, err)
			}
			if short {
				continue
			}
			v.Title, v.Description, v.Published = f.Title, f.Description, f.Published
		}
		listed = append(listed, v)
	}
	n, err := s.Store.BackfillYouTubePlaylist(showID, listed)
	if err != nil {
		return err
	}
	if n > 0 {
		logger.Info("SYNC youtube show tmdb=%d source=%s playlist listed %d episodes", tmdbID, source, n)
	}
	return nil
}

func backfillKey(showID int64) string { return fmt.Sprintf("ytbackfill:%d", showID) }

// startChannelBackfill files a channel's whole upload history in the
// background, once per show, the first time the show is synced.
func (s *Syncer) startChannelBackfill(showID int64, tmdbID int64, source string) {
	if youtube.UploadsPlaylist(source) == "" || s.Store.KVGet(backfillKey(showID)) != "" {
		return
	}
	s.backfillMu.Lock()
	if s.backfilling == nil {
		s.backfilling = map[int64]bool{}
	}
	if s.backfilling[showID] {
		s.backfillMu.Unlock()
		return
	}
	s.backfilling[showID] = true
	s.backfillMu.Unlock()
	go func() {
		defer func() {
			s.backfillMu.Lock()
			delete(s.backfilling, showID)
			s.backfillMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
		defer cancel()
		if _, err := s.BackfillYouTubeChannel(ctx, showID, source); err != nil {
			logger.Error("SYNC youtube background backfill show=%d source=%s, will retry on a later poll: %v", showID, source, err)
			if e := s.Store.RecordSyncError(tmdbID, trunc("backfill: "+err.Error(), 300)); e != nil {
				logger.Error("SYNC %v", e)
			}
		}
	}()
}

const (
	youtubeDateWorkers = 8
	youtubeDateRetries = 2
)

// BackfillYouTubeChannel lists every upload of a channel and files them all
// under its show with their exact publish times, so the season and episode
// numbers are right before anything is marked watched. A channel is only
// filed when its dates could be read for all but a small share of the videos.
func (s *Syncer) BackfillYouTubeChannel(ctx context.Context, showID int64, source string) (int, error) {
	if s.YouTube == nil {
		return 0, errors.New("youtube backfill: not configured")
	}
	l := s.lockFor(backfillKey(showID))
	l.Lock()
	defer l.Unlock()
	uploads := youtube.UploadsPlaylist(source)
	if uploads == "" {
		return 0, fmt.Errorf("youtube backfill show=%d: %q is not a channel", showID, source)
	}
	entries, err := s.YouTube.PlaylistVideos(ctx, uploads, 0)
	if err != nil {
		return 0, fmt.Errorf("youtube backfill show=%d source=%s listing: %w", showID, source, err)
	}
	known, err := s.Store.KnownYouTubeVideos(showID)
	if err != nil {
		return 0, err
	}
	var todo []youtube.PlaylistEntry
	for _, e := range entries {
		if !known[e.ID] {
			todo = append(todo, e)
		}
	}
	if len(todo) == 0 {
		return 0, nil
	}
	type result struct {
		at  time.Time
		err error
	}
	results := make([]result, len(todo))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < youtubeDateWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				var at time.Time
				var err error
				for try := 0; try <= youtubeDateRetries; try++ {
					if at, err = s.YouTube.PublishedAt(ctx, todo[i].ID); err == nil || ctx.Err() != nil {
						break
					}
					select {
					case <-ctx.Done():
					case <-time.After(time.Duration(try+1) * time.Second):
					}
				}
				results[i] = result{at, err}
			}
		}()
	}
	for i := range todo {
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("youtube backfill show=%d source=%s stopped: %w", showID, source, err)
	}
	vids := make([]store.YouTubeVideo, 0, len(todo))
	failed := 0
	for i, r := range results {
		if r.err != nil {
			failed++
			logger.Warn("SYNC youtube backfill show=%d video=%s date unreadable: %v", showID, todo[i].ID, r.err)
			continue
		}
		vids = append(vids, store.YouTubeVideo{ID: todo[i].ID, Title: todo[i].Title, Published: r.at})
	}
	if failed*50 > len(todo) {
		return 0, fmt.Errorf("youtube backfill show=%d source=%s: could not read the date of %d of %d videos", showID, source, failed, len(todo))
	}
	n, err := s.Store.BackfillYouTubeChannel(showID, vids)
	if err != nil {
		return 0, err
	}
	if err := s.Store.KVSet(backfillKey(showID), "1"); err != nil {
		logger.Error("SYNC youtube show=%d source=%s backfill done but not recorded, it will run again: %v", showID, source, err)
	}
	logger.Info("SYNC youtube show=%d source=%s backfilled %d of %d uploads (%d dates unreadable)", showID, source, n, len(entries), failed)
	return n, nil
}

func (s *Syncer) isShort(ctx context.Context, videoID string) (bool, error) {
	s.shortMu.Lock()
	short, ok := s.shorts[videoID]
	s.shortMu.Unlock()
	if ok {
		return short, nil
	}
	short, err := s.YouTube.IsShort(ctx, videoID)
	if err != nil {
		return false, err
	}
	s.shortMu.Lock()
	if len(s.shorts) >= youtubeShortCache {
		s.shorts = map[string]bool{}
	}
	s.shorts[videoID] = short
	s.shortMu.Unlock()
	return short, nil
}

// RunYouTube polls the feed of every followed source-fed show.
func (s *Syncer) RunYouTube(ctx context.Context) {
	if s.YouTube == nil {
		logger.Warn("SYNC youtube polling disabled: no client")
		return
	}
	logger.Info("SYNC youtube poller started")
	s.pollYouTube(ctx)
	tick := time.NewTicker(youtubePollEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.pollYouTube(ctx)
		}
	}
}

func (s *Syncer) pollYouTube(ctx context.Context) {
	adoptions, err := s.Store.FollowedAdoptions()
	if err != nil {
		logger.Error("SYNC youtube list adoptions: %v", err)
	}
	for _, y := range adoptions {
		if err := s.Store.SetYouTubeSource(y.ID, y.Source); err != nil {
			logger.Error("SYNC youtube adopt show=%d %q source=%s: %v", y.ID, y.Name, y.Source, err)
			continue
		}
		logger.Info("SYNC youtube show=%d %q now follows source %s", y.ID, y.Name, y.Source)
	}
	shows, err := s.Store.FollowedYouTubeShows()
	if err != nil {
		logger.Error("SYNC youtube list followed shows: %v", err)
		return
	}
	for _, y := range shows {
		if ctx.Err() != nil {
			return
		}
		if _, _, err := s.SyncYouTubeShow(ctx, y.TMDBID); err != nil {
			logger.Error("SYNC youtube poll show=%d %q: %v", y.ID, y.Name, err)
			if e := s.Store.RecordSyncError(y.TMDBID, trunc(err.Error(), 300)); e != nil {
				logger.Error("SYNC %v", e)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(youtubePause):
		}
	}
}
