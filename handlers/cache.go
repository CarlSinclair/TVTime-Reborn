package handlers

import (
	"context"
	"strings"
	"sync"
	"time"

	"tvt/logger"
	"tvt/tmdb"
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

func (a *App) searchTV(ctx context.Context, q, lang string) (*tmdb.SearchResponse, error) {
	return a.cachedSearch(ctx, "tv", q, lang, a.TMDB.SearchTV)
}
