package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"tvt/logger"
	"tvt/store"
	"tvt/tmdb"
	"tvt/trakt"
	"tvt/tvdb"
	"tvt/tvmaze"
)

const (
	staleAfter       = 24 * time.Hour
	i18nRefreshAfter = 7 * 24 * time.Hour
	canonicalLang    = "en-US"
)

type Syncer struct {
	Store    *store.Store
	TMDB     *tmdb.Client
	TVmaze   *tvmaze.Client
	Trakt    *trakt.Client
	TVDB     *tvdb.Client
	Now      func() time.Time
	mu       sync.Mutex
	locks    map[string]*sync.Mutex
	failed   map[string]time.Time
	prefetch sync.Mutex
}

func New(st *store.Store, c *tmdb.Client) *Syncer {
	return &Syncer{Store: st, TMDB: c, locks: map[string]*sync.Mutex{}, failed: map[string]time.Time{}}
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Syncer) lockFor(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[key]
	if !ok {
		l = &sync.Mutex{}
		s.locks[key] = l
	}
	return l
}

func names(n []tmdb.Named) string {
	out := make([]string, 0, len(n))
	for _, x := range n {
		out = append(out, x.Name)
	}
	return strings.Join(out, ", ")
}

func avgRuntime(r []int) int {
	if len(r) == 0 {
		return 0
	}
	t := 0
	for _, v := range r {
		t += v
	}
	return t / len(r)
}

// SyncShow fetches one show and all of its seasons from TMDB (canonical
// English) and stores them. A per-show mutex keeps two simultaneous opens of an
// unsynced show from fetching it twice.
func (s *Syncer) SyncShow(ctx context.Context, tmdbID int64) (*store.Show, error) {
	if tmdbID < 0 {
		return s.SyncTVDBShow(ctx, -tmdbID)
	}
	l := s.lockFor(fmt.Sprintf("show:%d", tmdbID))
	l.Lock()
	defer l.Unlock()

	d, err := s.TMDB.ShowDetails(ctx, tmdbID, canonicalLang)
	if err != nil {
		return nil, fmt.Errorf("sync show tmdb=%d details: %w", tmdbID, err)
	}
	numbers := make([]int, 0, len(d.Seasons))
	for _, se := range d.Seasons {
		numbers = append(numbers, se.SeasonNumber)
	}
	details, err := s.TMDB.Seasons(ctx, tmdbID, numbers, canonicalLang)
	if err != nil {
		return nil, fmt.Errorf("sync show tmdb=%d seasons: %w", tmdbID, err)
	}
	inputs := make([]store.SeasonInput, 0, len(d.Seasons))
	for _, stub := range d.Seasons {
		in := store.SeasonInput{
			Number: stub.SeasonNumber, Name: stub.Name, Overview: stub.Overview,
			PosterPath: stub.PosterPath, AirDate: stub.AirDate, EpisodeCount: stub.EpisodeCount,
		}
		if sd, ok := details[stub.SeasonNumber]; ok {
			if sd.Name != "" {
				in.Name = sd.Name
			}
			for _, ep := range sd.Episodes {
				in.Episodes = append(in.Episodes, store.EpisodeInput{
					TMDBID: ep.ID, SeasonNumber: ep.SeasonNumber, EpisodeNumber: ep.EpisodeNumber, Name: ep.Name,
					Overview: ep.Overview, AirDate: ep.AirDate, Runtime: ep.Runtime, StillPath: ep.StillPath,
					VoteAverage: ep.VoteAverage, VoteCount: ep.VoteCount,
				})
			}
			in.EpisodeCount = len(in.Episodes)
		}
		inputs = append(inputs, in)
	}
	sh := &store.Show{
		TMDBID: d.ID, TVDBID: d.ExternalIDs.TVDBID, IMDbID: d.ExternalIDs.IMDbID, Name: d.DisplayName(), OriginalName: d.OriginalName,
		Overview: d.Overview, Status: d.Status, FirstAirDate: d.FirstAirDate, LastAirDate: d.LastAirDate,
		PosterPath: d.PosterPath, BackdropPath: d.BackdropPath, VoteAverage: d.VoteAverage, VoteCount: d.VoteCount,
		Genres: names(d.Genres), Networks: names(d.Networks), OriginalLanguage: d.OriginalLanguage,
		NumberOfSeasons: d.NumberOfSeasons, NumberOfEpisodes: d.NumberOfEpisodes, Runtime: avgRuntime(d.EpisodeRunTime),
	}
	id, err := s.Store.SaveShow(sh, inputs)
	if err != nil {
		return nil, err
	}
	logger.Info("SYNC show tmdb=%d id=%d name=%q seasons=%d", tmdbID, id, sh.Name, len(inputs))
	if n, err := s.Store.CompleteFinished(0, id); err != nil {
		logger.Error("SYNC complete finished show=%d: %v", id, err)
	} else if n > 0 {
		logger.Info("SYNC marked %d follows of show=%d completed", n, id)
	}
	saved, err := s.Store.ShowByID(id)
	if err != nil {
		return nil, err
	}
	s.enrichAirTimes(ctx, saved)
	return saved, nil
}

func (s *Syncer) EnsureShow(ctx context.Context, tmdbID int64) (*store.Show, error) {
	sh, err := s.Store.ShowByTMDB(tmdbID)
	if err == nil {
		return sh, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return s.SyncShow(ctx, tmdbID)
}

// EnsureShowLang makes sure a translation of the show exists for lang. A first
// view waits for it (bounded by ctx); a stale one is refreshed in the
// background so the page never blocks on a weekly refresh.
func (s *Syncer) EnsureShowLang(ctx context.Context, sh *store.Show, lang string) {
	if lang == "en" || !s.TMDB.Configured() || sh.TMDBID < 0 {
		return
	}
	exists, at, err := s.Store.ShowI18nAge(sh.ID, lang)
	if err != nil {
		logger.Error("SYNC i18n age show=%d lang=%s: %v", sh.ID, lang, err)
		return
	}
	switch {
	case !exists:
		if err := s.syncShowLang(ctx, sh, lang); err != nil {
			logger.Error("SYNC localize show=%d lang=%s: %v", sh.ID, lang, err)
		}
	case time.Since(store.ParseTs(at)) > i18nRefreshAfter:
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := s.syncShowLang(bg, sh, lang); err != nil {
				logger.Error("SYNC refresh localization show=%d lang=%s: %v", sh.ID, lang, err)
			}
		}()
	}
}

func (s *Syncer) syncShowLang(ctx context.Context, sh *store.Show, lang string) error {
	l := s.lockFor(fmt.Sprintf("showlang:%d:%s", sh.ID, lang))
	l.Lock()
	defer l.Unlock()
	tl := tmdb.Lang(lang)
	d, err := s.TMDB.ShowDetails(ctx, sh.TMDBID, tl)
	if err != nil {
		return fmt.Errorf("localized details: %w", err)
	}
	numbers := make([]int, 0, len(d.Seasons))
	for _, se := range d.Seasons {
		numbers = append(numbers, se.SeasonNumber)
	}
	details, err := s.TMDB.Seasons(ctx, sh.TMDBID, numbers, tl)
	if err != nil {
		return fmt.Errorf("localized seasons: %w", err)
	}
	var eps []store.EpisodeI18nInput
	for _, sd := range details {
		for _, ep := range sd.Episodes {
			eps = append(eps, store.EpisodeI18nInput{Season: ep.SeasonNumber, Number: ep.EpisodeNumber, Name: ep.Name, Overview: ep.Overview})
		}
	}
	name := d.Name
	if d.Name == d.OriginalName && !strings.EqualFold(d.OriginalLanguage, lang) {
		name = ""
	}
	return s.Store.SaveShowI18n(sh.ID, lang, name, d.Overview, eps)
}

func (s *Syncer) Run(ctx context.Context) {
	if !s.TMDB.Configured() {
		logger.Warn("SYNC TMDB key not configured, background sync disabled")
		return
	}
	logger.Info("SYNC worker started")
	queue := time.NewTicker(time.Minute)
	poll := time.NewTicker(time.Hour)
	defer queue.Stop()
	defer poll.Stop()
	s.pollChanges(ctx)
	s.drainQueue(ctx)
	s.fillLocalizations(ctx)
	for {
		select {
		case <-ctx.Done():
			logger.Info("SYNC worker stopped")
			return
		case <-poll.C:
			s.pollChanges(ctx)
			s.fillLocalizations(ctx)
		case <-queue.C:
			s.drainQueue(ctx)
		}
	}
}

func (s *Syncer) cursor() (time.Time, time.Time) {
	end := time.Now().UTC()
	start := end.Add(-24 * time.Hour)
	if last := s.Store.SyncStateGet("changes_last"); last != "" {
		if t, err := time.Parse("2006-01-02", last); err == nil && t.After(end.AddDate(0, 0, -13)) {
			start = t
		}
	}
	return start, end
}

func (s *Syncer) pollChanges(ctx context.Context) {
	start, end := s.cursor()
	tvIDs, err := s.TMDB.ChangedTV(ctx, start, end)
	if err != nil {
		logger.Error("SYNC tv changes feed start=%s end=%s: %v", start.Format("2006-01-02"), end.Format("2006-01-02"), err)
		return
	}
	marked, err := s.Store.MarkNeedsSync(tvIDs)
	if err != nil {
		logger.Error("SYNC mark changed shows: %v", err)
		return
	}
	stale, err := s.Store.MarkStaleFollowedForSync(store.Ts(end.Add(-staleAfter)))
	if err != nil {
		logger.Error("SYNC mark stale followed: %v", err)
	}
	if err := s.Store.SyncStateSet("changes_last", end.Format("2006-01-02")); err != nil {
		logger.Error("SYNC remember changes cursor: %v", err)
	}
	logger.Info("SYNC changes feed tv=%d marked=%d stale_followed=%d", len(tvIDs), marked, stale)
}

func (s *Syncer) drainQueue(ctx context.Context) {
	ids, err := s.Store.ShowsNeedingSync(40)
	if err != nil {
		logger.Error("SYNC list show queue: %v", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.SyncShow(ctx, id); err != nil {
			logger.Error("SYNC queued show tmdb=%d: %v", id, err)
			msg := trunc(err.Error(), 300)
			if errors.Is(err, tmdb.ErrNotFound) {
				msg = "not found on TMDB"
			}
			if e := s.Store.RecordSyncError(id, msg); e != nil {
				logger.Error("SYNC %v", e)
			}
		}
	}
}

const (
	langRetryAfter = 15 * time.Minute
	langBatch      = 200
	langPause      = 300 * time.Millisecond
)

// fillLocalizations fetches every followed show in the language of each user
// who follows it, so titles and synopses are already stored when they are
// needed, including the ones used in notifications. A failed fetch is retried
// after langRetryAfter rather than on every pass.
func (s *Syncer) fillLocalizations(ctx context.Context) {
	if !s.TMDB.Configured() {
		return
	}
	due, err := s.Store.LocalizationsDue(store.Ts(s.now().Add(-i18nRefreshAfter)), langBatch)
	if err != nil {
		logger.Error("SYNC list due localizations: %v", err)
		return
	}
	for _, d := range due {
		if ctx.Err() != nil {
			return
		}
		key := fmt.Sprintf("%d:%s", d.ShowID, d.Lang)
		s.mu.Lock()
		at, bad := s.failed[key]
		s.mu.Unlock()
		if bad && s.now().Sub(at) < langRetryAfter {
			continue
		}
		if !sleepCtx(ctx, langPause) {
			return
		}
		if err := s.syncShowLang(ctx, &store.Show{ID: d.ShowID, TMDBID: d.TMDBID}, d.Lang); err != nil {
			logger.Error("SYNC prefetch localization show=%d lang=%s: %v", d.ShowID, d.Lang, err)
			s.mu.Lock()
			s.failed[key] = s.now()
			s.mu.Unlock()
			continue
		}
		s.mu.Lock()
		delete(s.failed, key)
		s.mu.Unlock()
	}
}

// PrefetchLanguages starts one background pass over followed shows, for use
// when a user changes language. A pass already running is not doubled up.
func (s *Syncer) PrefetchLanguages() {
	if !s.TMDB.Configured() {
		return
	}
	go func() {
		if !s.prefetch.TryLock() {
			return
		}
		defer s.prefetch.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		s.fillLocalizations(ctx)
	}()
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

const providerRetryAfter = 7 * 24 * time.Hour

func terminalStatus(status string) bool { return status == "Ended" || status == "Canceled" }

// enrichAirTimes adds real broadcast times, best effort: failures are logged
// and the show stays usable with TMDB's dates alone. TVmaze goes first; Trakt
// (when configured) only fills episodes TVmaze left without a time. A show a
// provider does not know is not looked up again for a week.
func (s *Syncer) enrichAirTimes(ctx context.Context, sh *store.Show) {
	if sh.TMDBID < 0 {
		return
	}
	if terminalStatus(sh.Status) {
		return
	}
	s.enrichTVmaze(ctx, sh)
	s.enrichTrakt(ctx, sh)
}

func (s *Syncer) recentlyChecked(showID int64, provider string) (id int64, skip bool) {
	id, checked, err := s.Store.ProviderState(showID, provider)
	if err != nil {
		logger.Error("SYNC %s state show=%d: %v", provider, showID, err)
		return 0, true
	}
	if id == 0 && checked != "" && time.Since(store.ParseTs(checked)) < providerRetryAfter {
		return 0, true
	}
	return id, false
}

func (s *Syncer) enrichTVmaze(ctx context.Context, sh *store.Show) {
	if s.TVmaze == nil {
		return
	}
	id, skip := s.recentlyChecked(sh.ID, store.SourceTVmaze)
	if skip {
		return
	}
	var err error
	if id == 0 {
		id, err = s.TVmaze.LookupShow(ctx, sh.IMDbID, sh.TVDBID)
		if errors.Is(err, tvmaze.ErrNotFound) {
			if e := s.Store.MarkProviderChecked(sh.ID, store.SourceTVmaze); e != nil {
				logger.Error("SYNC %v", e)
			}
			logger.Info("SYNC tvmaze has no match for show=%d %q", sh.ID, sh.Name)
			return
		}
		if err != nil {
			logger.Error("SYNC tvmaze lookup show=%d: %v", sh.ID, err)
			return
		}
	}
	eps, err := s.TVmaze.Episodes(ctx, id)
	if err != nil {
		logger.Error("SYNC tvmaze episodes show=%d tvmaze=%d: %v", sh.ID, id, err)
		return
	}
	times := make([]store.AirTime, 0, len(eps))
	for _, e := range eps {
		if !e.Airstamp.IsZero() {
			times = append(times, store.AirTime{Season: e.Season, Number: e.Number, AirDate: e.Airdate, AirAt: e.Airstamp})
		}
	}
	applied, err := s.Store.SaveAirTimes(sh.ID, store.SourceTVmaze, id, times)
	if err != nil {
		logger.Error("SYNC save tvmaze air times show=%d: %v", sh.ID, err)
		return
	}
	logger.Info("SYNC tvmaze show=%d tvmaze=%d air times applied=%d of %d", sh.ID, id, applied, len(eps))
}

func (s *Syncer) enrichTrakt(ctx context.Context, sh *store.Show) {
	if s.Trakt == nil {
		return
	}
	need, err := s.Store.HasUntimedRecent(sh.ID, s.now())
	if err != nil {
		logger.Error("SYNC %v", err)
		return
	}
	if !need {
		return
	}
	id, skip := s.recentlyChecked(sh.ID, store.SourceTrakt)
	if skip {
		return
	}
	if id == 0 {
		id, err = s.Trakt.FindShowByTMDB(ctx, sh.TMDBID)
		if errors.Is(err, trakt.ErrNotFound) {
			if e := s.Store.MarkProviderChecked(sh.ID, store.SourceTrakt); e != nil {
				logger.Error("SYNC %v", e)
			}
			logger.Info("SYNC trakt has no match for show=%d %q", sh.ID, sh.Name)
			return
		}
		if err != nil {
			logger.Error("SYNC trakt lookup show=%d: %v", sh.ID, err)
			return
		}
	}
	eps, err := s.Trakt.Episodes(ctx, id)
	if err != nil {
		logger.Error("SYNC trakt episodes show=%d trakt=%d: %v", sh.ID, id, err)
		return
	}
	times := make([]store.AirTime, 0, len(eps))
	for _, e := range eps {
		times = append(times, store.AirTime{Season: e.Season, Number: e.Number, AirDate: e.FirstAired.Format("2006-01-02"), AirAt: e.FirstAired})
	}
	applied, err := s.Store.SaveAirTimes(sh.ID, store.SourceTrakt, id, times)
	if err != nil {
		logger.Error("SYNC save trakt air times show=%d: %v", sh.ID, err)
		return
	}
	logger.Info("SYNC trakt show=%d trakt=%d air times applied=%d of %d", sh.ID, id, applied, len(eps))
}
