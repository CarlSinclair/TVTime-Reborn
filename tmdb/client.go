package tmdb

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

const DefaultBase = "https://api.themoviedb.org/3"

var ErrNotFound = errors.New("tmdb: not found")
var ErrNoKey = errors.New("tmdb: API key not configured")

type Client struct {
	Base string
	key  string
	http *http.Client
	tick <-chan time.Time
}

func New(key string) *Client {
	return &Client{
		Base: DefaultBase,
		key:  strings.TrimSpace(key),
		http: &http.Client{Timeout: 20 * time.Second},
		tick: time.Tick(50 * time.Millisecond),
	}
}

func (c *Client) Configured() bool { return c.key != "" }

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	if !c.Configured() {
		return ErrNoKey
	}
	if q == nil {
		q = url.Values{}
	}
	bearer := strings.HasPrefix(c.key, "eyJ")
	if !bearer {
		q.Set("api_key", c.key)
	}
	full := c.Base + path
	if enc := q.Encode(); enc != "" {
		full += "?" + enc
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.tick:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
		if err != nil {
			return fmt.Errorf("tmdb request %s: %w", path, err)
		}
		req.Header.Set("Accept", "application/json")
		if bearer {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("tmdb GET %s: %s", path, strings.ReplaceAll(err.Error(), c.key, "<key>"))
			logger.Warn("TMDB attempt=%d %v", attempt+1, lastErr)
			if !sleep(ctx, backoff(attempt)) {
				return ctx.Err()
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return ErrNotFound
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			wait := backoff(attempt)
			if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 {
				wait = time.Duration(ra) * time.Second
			}
			lastErr = fmt.Errorf("tmdb GET %s: status %d", path, resp.StatusCode)
			logger.Warn("TMDB attempt=%d %v retry_in=%s", attempt+1, lastErr, wait)
			if !sleep(ctx, wait) {
				return ctx.Err()
			}
			continue
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("tmdb GET %s: status %d: %s", path, resp.StatusCode, trunc(string(body), 200))
		}
		if readErr != nil {
			lastErr = fmt.Errorf("tmdb read %s: %w", path, readErr)
			continue
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("tmdb decode %s: %w", path, err)
		}
		return nil
	}
	return lastErr
}

func backoff(attempt int) time.Duration { return time.Duration(1<<attempt) * 500 * time.Millisecond }

func sleep(ctx context.Context, d time.Duration) bool {
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

var LangMap = map[string]string{
	"en": "en-US", "pt": "pt-BR", "es": "es-ES", "it": "it-IT", "fr": "fr-FR",
	"zh": "zh-CN", "ru": "ru-RU", "ja": "ja-JP", "ko": "ko-KR", "ar": "ar-SA",
}

func Lang(appLang string) string {
	if l, ok := LangMap[appLang]; ok {
		return l
	}
	return "en-US"
}

type SearchItem struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	OriginalName string  `json:"original_name"`
	Overview     string  `json:"overview"`
	FirstAirDate string  `json:"first_air_date"`
	PosterPath   string  `json:"poster_path"`
	VoteAverage  float64 `json:"vote_average"`
}

type SearchResponse struct {
	Page         int          `json:"page"`
	TotalPages   int          `json:"total_pages"`
	TotalResults int          `json:"total_results"`
	Results      []SearchItem `json:"results"`
}

func (c *Client) SearchTV(ctx context.Context, query string, page int, lang string) (*SearchResponse, error) {
	q := url.Values{"query": {query}, "page": {strconv.Itoa(page)}, "include_adult": {"false"}, "language": {lang}}
	var out SearchResponse
	if err := c.get(ctx, "/search/tv", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) TrendingTV(ctx context.Context, lang string) (*SearchResponse, error) {
	var out SearchResponse
	if err := c.get(ctx, "/trending/tv/week", url.Values{"language": {lang}}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type Named struct {
	Name string `json:"name"`
}

type SeasonStub struct {
	SeasonNumber int    `json:"season_number"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	PosterPath   string `json:"poster_path"`
	AirDate      string `json:"air_date"`
	EpisodeCount int    `json:"episode_count"`
}

type ExternalIDs struct {
	IMDbID string `json:"imdb_id"`
	TVDBID int64  `json:"tvdb_id"`
}

type ShowDetails struct {
	ID               int64        `json:"id"`
	Name             string       `json:"name"`
	OriginalName     string       `json:"original_name"`
	Overview         string       `json:"overview"`
	Status           string       `json:"status"`
	FirstAirDate     string       `json:"first_air_date"`
	LastAirDate      string       `json:"last_air_date"`
	PosterPath       string       `json:"poster_path"`
	BackdropPath     string       `json:"backdrop_path"`
	VoteAverage      float64      `json:"vote_average"`
	VoteCount        int          `json:"vote_count"`
	Genres           []Named      `json:"genres"`
	Networks         []Named      `json:"networks"`
	OriginalLanguage string       `json:"original_language"`
	NumberOfSeasons  int          `json:"number_of_seasons"`
	NumberOfEpisodes int          `json:"number_of_episodes"`
	EpisodeRunTime   []int        `json:"episode_run_time"`
	Seasons          []SeasonStub `json:"seasons"`
	ExternalIDs      ExternalIDs  `json:"external_ids"`
}

func (c *Client) ShowDetails(ctx context.Context, id int64, lang string) (*ShowDetails, error) {
	var out ShowDetails
	q := url.Values{"language": {lang}, "append_to_response": {"external_ids"}}
	if err := c.get(ctx, "/tv/"+strconv.FormatInt(id, 10), q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type EpisodeItem struct {
	ID            int64   `json:"id"`
	SeasonNumber  int     `json:"season_number"`
	EpisodeNumber int     `json:"episode_number"`
	Name          string  `json:"name"`
	Overview      string  `json:"overview"`
	AirDate       string  `json:"air_date"`
	Runtime       int     `json:"runtime"`
	StillPath     string  `json:"still_path"`
	VoteAverage   float64 `json:"vote_average"`
	VoteCount     int     `json:"vote_count"`
}

type SeasonDetails struct {
	SeasonNumber int           `json:"season_number"`
	Name         string        `json:"name"`
	Overview     string        `json:"overview"`
	PosterPath   string        `json:"poster_path"`
	AirDate      string        `json:"air_date"`
	Episodes     []EpisodeItem `json:"episodes"`
}

// Seasons fetches up to 20 seasons of one show in a single request using
// append_to_response, which is how TMDB batches sub-resources.
func (c *Client) Seasons(ctx context.Context, showID int64, numbers []int, lang string) (map[int]*SeasonDetails, error) {
	out := map[int]*SeasonDetails{}
	for i := 0; i < len(numbers); i += 20 {
		end := i + 20
		if end > len(numbers) {
			end = len(numbers)
		}
		parts := make([]string, 0, end-i)
		for _, n := range numbers[i:end] {
			parts = append(parts, "season/"+strconv.Itoa(n))
		}
		var raw map[string]json.RawMessage
		q := url.Values{"language": {lang}, "append_to_response": {strings.Join(parts, ",")}}
		if err := c.get(ctx, "/tv/"+strconv.FormatInt(showID, 10), q, &raw); err != nil {
			return nil, err
		}
		for _, n := range numbers[i:end] {
			blob, ok := raw["season/"+strconv.Itoa(n)]
			if !ok {
				logger.Warn("TMDB show=%d season=%d missing from append_to_response", showID, n)
				continue
			}
			var sd SeasonDetails
			if err := json.Unmarshal(blob, &sd); err != nil {
				return nil, fmt.Errorf("tmdb decode show=%d season=%d: %w", showID, n, err)
			}
			out[n] = &sd
		}
	}
	return out, nil
}

type changesResponse struct {
	Results []struct {
		ID int64 `json:"id"`
	} `json:"results"`
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
}

func (c *Client) ChangedTV(ctx context.Context, start, end time.Time) ([]int64, error) {
	return c.changed(ctx, "/tv/changes", start, end)
}

func (c *Client) changed(ctx context.Context, path string, start, end time.Time) ([]int64, error) {
	var ids []int64
	for page := 1; ; page++ {
		q := url.Values{
			"start_date": {start.Format("2006-01-02")},
			"end_date":   {end.Format("2006-01-02")},
			"page":       {strconv.Itoa(page)},
		}
		var out changesResponse
		if err := c.get(ctx, path, q, &out); err != nil {
			return nil, err
		}
		for _, r := range out.Results {
			ids = append(ids, r.ID)
		}
		if page >= out.TotalPages || page >= 50 {
			break
		}
	}
	return ids, nil
}

type FindResponse struct {
	TVResults        []SearchItem  `json:"tv_results"`
	TVEpisodeResults []EpisodeFind `json:"tv_episode_results"`
}

type EpisodeFind struct {
	ID            int64 `json:"id"`
	ShowID        int64 `json:"show_id"`
	SeasonNumber  int   `json:"season_number"`
	EpisodeNumber int   `json:"episode_number"`
}

// FindEpisodeByExternal resolves an external (for example TVDB) episode id to
// the TMDB episode, which carries its show and numbering.
func (c *Client) FindEpisodeByExternal(ctx context.Context, id, source string) (*EpisodeFind, error) {
	var out FindResponse
	if err := c.get(ctx, "/find/"+url.PathEscape(id), url.Values{"external_source": {source}}, &out); err != nil {
		return nil, err
	}
	if len(out.TVEpisodeResults) == 0 {
		return nil, ErrNotFound
	}
	return &out.TVEpisodeResults[0], nil
}

// FindByExternal resolves an IMDb or TVDB id to a TMDB show, used by importers.
func (c *Client) FindByExternal(ctx context.Context, id, source string) (*SearchItem, error) {
	var out FindResponse
	if err := c.get(ctx, "/find/"+url.PathEscape(id), url.Values{"external_source": {source}}, &out); err != nil {
		return nil, err
	}
	if len(out.TVResults) == 0 {
		return nil, ErrNotFound
	}
	return &out.TVResults[0], nil
}
