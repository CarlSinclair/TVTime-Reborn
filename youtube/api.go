package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	apiBase     = "https://www.googleapis.com/youtube/v3"
	maxAPIPages = 400
)

type API struct {
	key  string
	HTTP *http.Client
	Base string
}

func NewAPI(key string) *API {
	return &API{key: strings.TrimSpace(key), HTTP: &http.Client{Timeout: 20 * time.Second}, Base: apiBase}
}

type APIError struct {
	Status  int
	Reason  string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("youtube api: status %d reason=%s: %s", e.Status, e.Reason, e.Message)
}

func IsAPIQuota(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	switch e.Reason {
	case "quotaExceeded", "rateLimitExceeded", "dailyLimitExceeded", "userRateLimitExceeded":
		return true
	}
	return false
}

type playlistItemsReply struct {
	NextPageToken string `json:"nextPageToken"`
	Items         []struct {
		Snippet struct {
			Title string `json:"title"`
		} `json:"snippet"`
		ContentDetails struct {
			VideoID          string `json:"videoId"`
			VideoPublishedAt string `json:"videoPublishedAt"`
		} `json:"contentDetails"`
	} `json:"items"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	} `json:"error"`
}

// PlaylistVideos lists every video of a playlist with its exact publish time,
// 50 per request. Videos that are private or deleted come back without a date
// and are listed in the entries but absent from the dates map.
func (a *API) PlaylistVideos(ctx context.Context, playlistID string) ([]PlaylistEntry, map[string]time.Time, error) {
	if a.key == "" {
		return nil, nil, errors.New("youtube api: no key configured")
	}
	if !listIDRe.MatchString(playlistID) {
		return nil, nil, fmt.Errorf("youtube api: %q is not a playlist id", playlistID)
	}
	var entries []PlaylistEntry
	dates := map[string]time.Time{}
	token := ""
	for page := 0; page < maxAPIPages; page++ {
		q := url.Values{"part": {"snippet,contentDetails"}, "maxResults": {"50"}, "playlistId": {playlistID}}
		if token != "" {
			q.Set("pageToken", token)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Base+"/playlistItems?"+q.Encode(), nil)
		if err != nil {
			return nil, nil, fmt.Errorf("youtube api playlist %s: %w", playlistID, err)
		}
		req.Header.Set("X-Goog-Api-Key", a.key)
		req.Header.Set("Accept", "application/json")
		resp, err := a.HTTP.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("youtube api playlist %s page %d: %w", playlistID, page+1, err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, nil, fmt.Errorf("youtube api playlist %s page %d read: %w", playlistID, page+1, readErr)
		}
		var r playlistItemsReply
		jsonErr := json.Unmarshal(raw, &r)
		if resp.StatusCode != http.StatusOK {
			apiErr := &APIError{Status: resp.StatusCode, Message: "no error detail in the reply"}
			if jsonErr == nil && r.Error != nil {
				apiErr.Message = r.Error.Message
				if len(r.Error.Errors) > 0 {
					apiErr.Reason = r.Error.Errors[0].Reason
				}
			}
			return nil, nil, fmt.Errorf("youtube api playlist %s page %d: %w", playlistID, page+1, apiErr)
		}
		if jsonErr != nil {
			return nil, nil, fmt.Errorf("youtube api playlist %s page %d: bad reply: %w", playlistID, page+1, jsonErr)
		}
		for _, it := range r.Items {
			id := it.ContentDetails.VideoID
			if !ValidVideoID(id) {
				continue
			}
			entries = append(entries, PlaylistEntry{ID: id, Title: it.Snippet.Title})
			if t, ok := parseStamp(it.ContentDetails.VideoPublishedAt); ok {
				dates[id] = t
			}
		}
		token = r.NextPageToken
		if token == "" {
			return entries, dates, nil
		}
	}
	return nil, nil, fmt.Errorf("youtube api playlist %s: more than %d pages", playlistID, maxAPIPages)
}
