package handlers

import (
	"context"
	"strings"
	"sync"
	"time"

	"tvt/logger"
	"tvt/syncer"
	"tvt/tmdb"
	"tvt/tvdb"
	"tvt/youtube"
)

type trendingEntry struct {
	items []tmdb.SearchItem
	at    time.Time
}

type trendingCache struct {
	mu   sync.Mutex
	byLn map[string]trendingEntry
}

func (c *trendingCache) get(ctx context.Context, lang string, fetch func(context.Context, string) (*tmdb.SearchResponse, error), what string) []tmdb.SearchItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byLn == nil {
		c.byLn = map[string]trendingEntry{}
	}
	e := c.byLn[lang]
	if len(e.items) > 0 && time.Since(e.at) < time.Hour {
		return e.items
	}
	res, err := fetch(ctx, tmdb.Lang(lang))
	if err != nil {
		logger.Error("TMDB trending %s lang=%s: %v", what, lang, err)
		return e.items
	}
	c.byLn[lang] = trendingEntry{items: res.Results, at: time.Now()}
	return res.Results
}

func (a *App) trendingShows(ctx context.Context, lang string) []tmdb.SearchItem {
	if !a.TMDB.Configured() {
		return nil
	}
	return a.trending.get(ctx, lang, a.TMDB.TrendingTV, "tv")
}

type searchEntry struct {
	res *tmdb.SearchResponse
	at  time.Time
}

var (
	searchMu    sync.Mutex
	searchCache = map[string]searchEntry{}
)

func (a *App) cachedSearch(ctx context.Context, kind, q, lang string, fetch func(context.Context, string, int, string) (*tmdb.SearchResponse, error)) (*tmdb.SearchResponse, error) {
	key := kind + "|" + lang + "|" + strings.ToLower(q)
	searchMu.Lock()
	if e, ok := searchCache[key]; ok && time.Since(e.at) < 10*time.Minute {
		searchMu.Unlock()
		return e.res, nil
	}
	searchMu.Unlock()
	res, err := fetch(ctx, q, 1, tmdb.Lang(lang))
	if err != nil {
		return nil, err
	}
	searchMu.Lock()
	if len(searchCache) > 500 {
		searchCache = map[string]searchEntry{}
	}
	searchCache[key] = searchEntry{res: res, at: time.Now()}
	searchMu.Unlock()
	return res, nil
}

type tvdbEntry struct {
	items []tvdb.SearchItem
	at    time.Time
}

var (
	tvdbMu    sync.Mutex
	tvdbCache = map[string]tvdbEntry{}
)

func (a *App) tvdbSearch(ctx context.Context, q string) []tvdb.SearchItem {
	key := strings.ToLower(q)
	tvdbMu.Lock()
	if e, ok := tvdbCache[key]; ok && time.Since(e.at) < 10*time.Minute {
		tvdbMu.Unlock()
		return e.items
	}
	tvdbMu.Unlock()
	items, err := a.Sync.TVDB.Search(ctx, q)
	if err != nil {
		logger.Warn("SEARCH tvdb q=%q: %v", q, err)
		return nil
	}
	tvdbMu.Lock()
	if len(tvdbCache) > 500 {
		tvdbCache = map[string]tvdbEntry{}
	}
	tvdbCache[key] = tvdbEntry{items: items, at: time.Now()}
	tvdbMu.Unlock()
	return items
}

func searchKey(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c > 127 {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// searchTV asks TMDB first. Shows TMDB does not have are added after its
// results: web series from TheTVDB when configured (YouTube ones are skipped,
// channels come from YouTube itself) and matching YouTube channels. A result
// whose title TMDB already returned is left out.
func (a *App) searchTV(ctx context.Context, q, lang string) (*tmdb.SearchResponse, error) {
	res, err := a.cachedSearch(ctx, "tv", q, lang, a.TMDB.SearchTV)
	if err != nil {
		return res, err
	}
	var extra []tmdb.SearchItem
	if a.Sync.TVDBConfigured() {
		for _, it := range a.tvdbSearch(ctx, q) {
			id := it.ID()
			if id == 0 || strings.EqualFold(it.Network, "YouTube") {
				continue
			}
			poster := ""
			if p := tvdb.ImagePath(it.ImageURL); p != "" {
				poster = "tvdb:" + p
			}
			extra = append(extra, tmdb.SearchItem{ID: -id, Name: it.Name, OriginalName: it.Name, Overview: it.Overview, FirstAirDate: it.FirstAirTime, PosterPath: poster})
		}
	}
	extra = append(extra, a.youtubeSearch(ctx, q)...)
	if len(extra) == 0 {
		return res, nil
	}
	known := map[string]bool{}
	for _, it := range res.Results {
		known[searchKey(it.Name)] = true
		known[searchKey(it.OriginalName)] = true
	}
	merged := *res
	merged.Results = append([]tmdb.SearchItem(nil), res.Results...)
	for _, it := range extra {
		if known[searchKey(it.Name)] {
			continue
		}
		merged.Results = append(merged.Results, it)
		known[searchKey(it.Name)] = true
		if len(merged.Results) >= 40 {
			break
		}
	}
	return &merged, nil
}

type youtubeEntry struct {
	items []tmdb.SearchItem
	at    time.Time
}

var (
	youtubeMu    sync.Mutex
	youtubeCache = map[string]youtubeEntry{}
)

const (
	youtubeSearchMinChars = 3
	youtubeCheckWorkers   = 5
)

// youtubeSearch lists every YouTube channel and playlist whose name matches the
// query (the name contains the query or the query contains the name), in
// YouTube's own relevance order. Each is checked against its real feed first, so
// a result that could not be followed, or whose feed is not what the page said
// it was, is left out. A result that is really a show already in the catalog
// gets that show's id.
func (a *App) youtubeSearch(ctx context.Context, q string) []tmdb.SearchItem {
	qk := searchKey(q)
	if !a.Sync.YouTubeConfigured() || len([]rune(qk)) < youtubeSearchMinChars {
		return nil
	}
	youtubeMu.Lock()
	if e, ok := youtubeCache[qk]; ok && time.Since(e.at) < 10*time.Minute {
		youtubeMu.Unlock()
		return e.items
	}
	youtubeMu.Unlock()
	found, err := a.Sync.YouTube.Search(ctx, q)
	if err != nil {
		logger.Warn("SEARCH youtube q=%q: %v", q, err)
	}
	var cands []youtube.Source
	for _, src := range found {
		tk := searchKey(src.Title)
		if tk != "" && (strings.Contains(tk, qk) || strings.Contains(qk, tk)) {
			cands = append(cands, src)
		}
	}
	results := make([]*syncer.YouTubeResult, len(cands))
	var wg sync.WaitGroup
	sem := make(chan struct{}, youtubeCheckWorkers)
	for i, src := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, src youtube.Source) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := a.Sync.CheckYouTubeSource(ctx, src)
			if err != nil {
				logger.Warn("SEARCH youtube q=%q dropped a result: %v", q, err)
				return
			}
			results[i] = res
		}(i, src)
	}
	wg.Wait()
	var items []tmdb.SearchItem
	for _, res := range results {
		if res == nil {
			continue
		}
		items = append(items, tmdb.SearchItem{ID: res.ID, Name: res.Name, OriginalName: res.Name, Overview: res.Overview, PosterPath: res.Poster})
	}
	if err == nil || len(found) > 0 {
		youtubeMu.Lock()
		if len(youtubeCache) > 500 {
			youtubeCache = map[string]youtubeEntry{}
		}
		youtubeCache[qk] = youtubeEntry{items: items, at: time.Now()}
		youtubeMu.Unlock()
	}
	return items
}
