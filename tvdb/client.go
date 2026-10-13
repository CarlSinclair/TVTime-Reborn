package tvdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"tvt/logger"
)

const DefaultBase = "https://api4.thetvdb.com/v4"

var (
	ErrNotFound = errors.New("tvdb: not found")
	ErrNoKey    = errors.New("tvdb: no API key configured")
)

type Client struct {
	Base string
	key  string
	http *http.Client
	tick <-chan time.Time

	mu      sync.Mutex
	token   string
	tokenAt time.Time
}

func New(key string) *Client {
	return &Client{Base: DefaultBase, key: strings.TrimSpace(key), http: &http.Client{Timeout: 25 * time.Second}, tick: time.Tick(150 * time.Millisecond)}
}

func (c *Client) Configured() bool { return c != nil && c.key != "" }

const tokenLife = 20 * 24 * time.Hour

func (c *Client) login(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.token != "" && time.Since(c.tokenAt) < tokenLife {
		return c.token, nil
	}
	body, err := json.Marshal(map[string]string{"apikey": c.key})
	if err != nil {
		return "", fmt.Errorf("tvdb login encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/login", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("tvdb login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("tvdb login: %s", strings.ReplaceAll(err.Error(), c.key, "<key>"))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tvdb login: status %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Data.Token == "" {
		return "", errors.New("tvdb login: no token in response")
	}
	c.token, c.tokenAt = out.Data.Token, time.Now()
	return c.token, nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	if !c.Configured() {
		return ErrNoKey
	}
	full := c.Base + path
	if enc := q.Encode(); enc != "" {
		full += "?" + enc
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.tick:
		}
		tok, err := c.login(ctx, attempt > 0 && errors.Is(lastErr, errUnauthorized))
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
		if err != nil {
			return fmt.Errorf("tvdb request %s: %w", path, err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("tvdb GET %s: %w", path, err)
			logger.Warn("TVDB attempt=%d %v", attempt+1, lastErr)
			time.Sleep(time.Duration(1<<attempt) * 500 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return ErrNotFound
		case resp.StatusCode == http.StatusUnauthorized:
			lastErr = errUnauthorized
			continue
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("tvdb GET %s: status %d", path, resp.StatusCode)
			logger.Warn("TVDB attempt=%d %v", attempt+1, lastErr)
			wait := time.Duration(1<<attempt) * time.Second
			if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 && ra < 30 {
				wait = time.Duration(ra) * time.Second
			}
			time.Sleep(wait)
			continue
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("tvdb GET %s: status %d", path, resp.StatusCode)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("tvdb GET %s: bad JSON: %w", path, err)
		}
		return nil
	}
	return lastErr
}

var errUnauthorized = errors.New("tvdb: unauthorized")

type SearchItem struct {
	TVDBID       string `json:"tvdb_id"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	Year         string `json:"year"`
	FirstAirTime string `json:"first_air_time"`
	ImageURL     string `json:"image_url"`
	Network      string `json:"network"`
	Status       string `json:"status"`
	Type         string `json:"type"`
}

func (s SearchItem) ID() int64 {
	n, _ := strconv.ParseInt(s.TVDBID, 10, 64)
	return n
}

func (c *Client) Search(ctx context.Context, query string) ([]SearchItem, error) {
	var out struct {
		Data []SearchItem `json:"data"`
	}
	if err := c.get(ctx, "/search", url.Values{"query": {query}, "type": {"series"}}, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

type Series struct {
	ID              int64                   `json:"id"`
	Name            string                  `json:"name"`
	Overview        string                  `json:"overview"`
	Image           string                  `json:"image"`
	FirstAired      string                  `json:"firstAired"`
	LastAired       string                  `json:"lastAired"`
	OriginalLang    string                  `json:"originalLanguage"`
	AverageRuntime  int                     `json:"averageRuntime"`
	Status          struct{ Name string }   `json:"status"`
	OriginalNetwork struct{ Name string }   `json:"originalNetwork"`
	Genres          []struct{ Name string } `json:"genres"`
	RemoteIDs       []struct {
		ID         string `json:"id"`
		SourceName string `json:"sourceName"`
	} `json:"remoteIds"`
}

func (s *Series) IMDbID() string {
	for _, r := range s.RemoteIDs {
		if strings.EqualFold(r.SourceName, "IMDB") {
			return r.ID
		}
	}
	return ""
}

func (c *Client) Series(ctx context.Context, id int64) (*Series, error) {
	var out struct {
		Data Series `json:"data"`
	}
	if err := c.get(ctx, "/series/"+strconv.FormatInt(id, 10)+"/extended", url.Values{"short": {"true"}}, &out); err != nil {
		return nil, err
	}
	if out.Data.ID == 0 {
		return nil, ErrNotFound
	}
	return &out.Data, nil
}

type Episode struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	Aired        string `json:"aired"`
	Runtime      int    `json:"runtime"`
	Image        string `json:"image"`
	IsMovie      int    `json:"isMovie"`
	Number       int    `json:"number"`
	SeasonNumber int    `json:"seasonNumber"`
}

const maxEpisodePages = 60

// Episodes returns every episode in aired order, following the paging.
func (c *Client) Episodes(ctx context.Context, id int64) ([]Episode, error) {
	var all []Episode
	for page := 0; page < maxEpisodePages; page++ {
		var out struct {
			Data struct {
				Episodes []Episode `json:"episodes"`
			} `json:"data"`
			Links struct {
				Next *string `json:"next"`
			} `json:"links"`
		}
		if err := c.get(ctx, "/series/"+strconv.FormatInt(id, 10)+"/episodes/default", url.Values{"page": {strconv.Itoa(page)}}, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Data.Episodes...)
		if out.Links.Next == nil || *out.Links.Next == "" || len(out.Data.Episodes) == 0 {
			break
		}
	}
	return all, nil
}

// ImagePath turns an artwork URL into the path under the artwork host, or ""
// when it is not one of TheTVDB's artwork URLs.
func ImagePath(u string) string {
	const prefix = "https://artworks.thetvdb.com"
	if strings.HasPrefix(u, prefix+"/") {
		return strings.TrimPrefix(u, prefix)
	}
	if strings.HasPrefix(u, "/banners/") {
		return u
	}
	return ""
}
