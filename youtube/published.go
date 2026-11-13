package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

type playerReply struct {
	Microformat struct {
		Renderer struct {
			PublishDate string `json:"publishDate"`
			UploadDate  string `json:"uploadDate"`
		} `json:"playerMicroformatRenderer"`
	} `json:"microformat"`
}

// PublishedAt is the exact moment a video was published, read from the small
// player reply instead of the 1.4 MB watch page.
func (c *Client) PublishedAt(ctx context.Context, videoID string) (time.Time, error) {
	if !ValidVideoID(videoID) {
		return time.Time{}, fmt.Errorf("youtube published: %q is not a video id", videoID)
	}
	body, err := json.Marshal(map[string]any{
		"context": map[string]any{"client": map[string]string{"clientName": "WEB", "clientVersion": c.clientVersion(), "hl": "en", "gl": "US"}},
		"videoId": videoID,
	})
	if err != nil {
		return time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.PlayerBase+"?prettyPrint=false", bytes.NewReader(body))
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("youtube published %s: %w", videoID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("youtube published %s: status %d", videoID, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return time.Time{}, fmt.Errorf("youtube published %s read: %w", videoID, err)
	}
	var r playerReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return time.Time{}, fmt.Errorf("youtube published %s: bad reply: %w", videoID, err)
	}
	stamp := r.Microformat.Renderer.PublishDate
	if stamp == "" {
		stamp = r.Microformat.Renderer.UploadDate
	}
	if t, ok := parseStamp(stamp); ok {
		return t, nil
	}
	return c.publishedFromPage(ctx, videoID)
}

func parseStamp(stamp string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, stamp); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

var pageDateRe = regexp.MustCompile(`"publishDate":"([^"]+)"|itemprop="datePublished" content="([^"]+)"`)

// publishedFromPage is the fallback when the player reply carries no date: the
// watch page names it twice, but is about 1.4 MB.
func (c *Client) publishedFromPage(ctx context.Context, videoID string) (time.Time, error) {
	resp, err := c.get(ctx, c.WatchBase+"/watch?v="+videoID)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("youtube published %s: watch page status %d", videoID, resp.StatusCode)
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return time.Time{}, fmt.Errorf("youtube published %s: watch page read: %w", videoID, err)
	}
	if m := pageDateRe.FindSubmatch(page); m != nil {
		stamp := string(m[1])
		if stamp == "" {
			stamp = string(m[2])
		}
		if t, ok := parseStamp(stamp); ok {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("youtube published %s: no publish date in the reply or the page", videoID)
}
