// Package tvmaze reads per-episode air times from the free TVmaze API. TMDB
// supplies only a date for each episode; TVmaze adds the broadcast time for
// shows where it is known.
package tvmaze

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tvt/logger"
)

const DefaultBase = "https://api.tvmaze.com"

var ErrNotFound = errors.New("tvmaze: not found")

type Client struct {
	Base string
	http *http.Client
	tick <-chan time.Time
}

// New limits itself to well under TVmaze's 20 calls per 10 seconds.
func New() *Client {
	return &Client{Base: DefaultBase, http: &http.Client{Timeout: 20 * time.Second}, tick: time.Tick(700 * time.Millisecond)}
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.tick:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
		if err != nil {
			return fmt.Errorf("tvmaze request %s: %w", path, err)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("tvmaze GET %s: %w", path, err)
			logger.Warn("TVMAZE attempt=%d %v", attempt+1, lastErr)
			time.Sleep(time.Duration(1<<attempt) * 500 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return ErrNotFound
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("tvmaze GET %s: status %d", path, resp.StatusCode)
			wait := time.Duration(1<<attempt) * time.Second
			if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 && ra < 30 {
				wait = time.Duration(ra) * time.Second
			}
			logger.Warn("TVMAZE attempt=%d %v retry_in=%s", attempt+1, lastErr, wait)
			time.Sleep(wait)
			continue
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("tvmaze GET %s: status %d", path, resp.StatusCode)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("tvmaze decode %s: %w", path, err)
		}
		return nil
	}
	return lastErr
}

// LookupShow finds a TVmaze show by IMDb id, then by TheTVDB id.
func (c *Client) LookupShow(ctx context.Context, imdb string, tvdb int64) (int64, error) {
	var paths []string
	if strings.TrimSpace(imdb) != "" {
		paths = append(paths, "/lookup/shows?imdb="+url.QueryEscape(imdb))
	}
	if tvdb > 0 {
		paths = append(paths, "/lookup/shows?thetvdb="+strconv.FormatInt(tvdb, 10))
	}
	for _, p := range paths {
		var s struct {
			ID int64 `json:"id"`
		}
		err := c.get(ctx, p, &s)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if s.ID > 0 {
			return s.ID, nil
		}
	}
	return 0, ErrNotFound
}

type Episode struct {
	Season   int
	Number   int
	Airdate  string
	Airtime  string
	Airstamp time.Time
}

type rawEpisode struct {
	Season   int    `json:"season"`
	Number   *int   `json:"number"`
	Airdate  string `json:"airdate"`
	Airtime  string `json:"airtime"`
	Airstamp string `json:"airstamp"`
}

// Episodes returns the numbered episodes of a show. Airstamp is set only when
// TVmaze has a real broadcast time (a non-empty airtime). Without one it fills
// the stamp with a placeholder of 12:00 UTC, which must not be mistaken for a
// release time.
func (c *Client) Episodes(ctx context.Context, id int64) ([]Episode, error) {
	var raw []rawEpisode
	if err := c.get(ctx, "/shows/"+strconv.FormatInt(id, 10)+"/episodes", &raw); err != nil {
		return nil, err
	}
	out := make([]Episode, 0, len(raw))
	for _, r := range raw {
		if r.Number == nil || r.Season <= 0 {
			continue
		}
		e := Episode{Season: r.Season, Number: *r.Number, Airdate: r.Airdate, Airtime: r.Airtime}
		if r.Airtime != "" && r.Airstamp != "" {
			if t, err := time.Parse(time.RFC3339, r.Airstamp); err == nil {
				e.Airstamp = t.UTC()
			}
		}
		out = append(out, e)
	}
	return out, nil
}
