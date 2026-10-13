// Package trakt reads episode first-aired timestamps from the Trakt API. It is
// an optional second source for air times, used when TVmaze has no real time.
package trakt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tvt/logger"
)

const DefaultBase = "https://api.trakt.tv"

var ErrNotFound = errors.New("trakt: not found")

type Client struct {
	Base     string
	ClientID string
	http     *http.Client
	tick     <-chan time.Time
}

func New(clientID string) *Client {
	return &Client{Base: DefaultBase, ClientID: strings.TrimSpace(clientID), http: &http.Client{Timeout: 20 * time.Second}, tick: time.Tick(400 * time.Millisecond)}
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
			return fmt.Errorf("trakt request %s: %w", path, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("trakt-api-version", "2")
		req.Header.Set("trakt-api-key", c.ClientID)
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("trakt GET %s: %s", path, strings.ReplaceAll(err.Error(), c.ClientID, "<key>"))
			logger.Warn("TRAKT attempt=%d %v", attempt+1, lastErr)
			time.Sleep(time.Duration(1<<attempt) * 500 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return ErrNotFound
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return fmt.Errorf("trakt GET %s: status %d (check TVT_TRAKT_CLIENT_ID)", path, resp.StatusCode)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("trakt GET %s: status %d", path, resp.StatusCode)
			wait := time.Duration(1<<attempt) * time.Second
			if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 && ra < 60 {
				wait = time.Duration(ra) * time.Second
			}
			logger.Warn("TRAKT attempt=%d %v retry_in=%s", attempt+1, lastErr, wait)
			time.Sleep(wait)
			continue
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("trakt GET %s: status %d", path, resp.StatusCode)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("trakt decode %s: %w", path, err)
		}
		return nil
	}
	return lastErr
}

// FindShowByTMDB returns Trakt's id for a TMDB show.
func (c *Client) FindShowByTMDB(ctx context.Context, tmdbID int64) (int64, error) {
	var res []struct {
		Type string `json:"type"`
		Show struct {
			IDs struct {
				Trakt int64 `json:"trakt"`
			} `json:"ids"`
		} `json:"show"`
	}
	if err := c.get(ctx, "/search/tmdb/"+strconv.FormatInt(tmdbID, 10)+"?type=show", &res); err != nil {
		return 0, err
	}
	for _, r := range res {
		if r.Type == "show" && r.Show.IDs.Trakt > 0 {
			return r.Show.IDs.Trakt, nil
		}
	}
	return 0, ErrNotFound
}

type Episode struct {
	Season, Number int
	FirstAired     time.Time
}

// plausible rejects timestamps that look like "date only" placeholders: exactly
// midnight or exactly noon UTC. A real release at those instants is possible
// but rare, and a wrong guess would alert hours early or late.
func plausible(t time.Time) bool {
	u := t.UTC()
	if u.Minute() != 0 || u.Second() != 0 {
		return true
	}
	return u.Hour() != 0 && u.Hour() != 12
}

// Episodes returns every numbered episode that has a believable first-aired
// instant.
func (c *Client) Episodes(ctx context.Context, traktID int64) ([]Episode, error) {
	var seasons []struct {
		Number   int `json:"number"`
		Episodes []struct {
			Season     *int    `json:"season"`
			Number     int     `json:"number"`
			FirstAired *string `json:"first_aired"`
		} `json:"episodes"`
	}
	if err := c.get(ctx, "/shows/"+strconv.FormatInt(traktID, 10)+"/seasons?extended=episodes,full", &seasons); err != nil {
		return nil, err
	}
	var out []Episode
	for _, s := range seasons {
		if s.Number <= 0 {
			continue
		}
		for _, e := range s.Episodes {
			if e.FirstAired == nil || *e.FirstAired == "" || e.Number <= 0 {
				continue
			}
			t, err := time.Parse(time.RFC3339, *e.FirstAired)
			if err != nil || !plausible(t) {
				continue
			}
			season := s.Number
			if e.Season != nil && *e.Season > 0 {
				season = *e.Season
			}
			out = append(out, Episode{Season: season, Number: e.Number, FirstAired: t.UTC()})
		}
	}
	return out, nil
}
