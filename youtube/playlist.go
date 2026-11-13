package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
)

const (
	maxPlaylistPages = 50
	fallbackVersion  = "2.20260101.00.00"
)

var clientVersionRe = regexp.MustCompile(`"clientVersion":"([0-9.]+)"`)

// PlaylistEntry is one video of a playlist as its page lists it. The page gives
// no exact publish time, so only the order and the title are known.
type PlaylistEntry struct {
	ID    string
	Title string
}

// UploadsPlaylist is the playlist that holds every upload of a channel, newest
// first.
func UploadsPlaylist(channelID string) string {
	if !ValidChannelID(channelID) {
		return ""
	}
	return "UU" + channelID[2:]
}

// PlaylistVideos lists every video of a playlist in the order the owner set,
// following the page's continuation until the list ends or maxPages pages have
// been read. The feed only holds the latest 15 entries; this is how the rest of
// a playlist is found.
func (c *Client) PlaylistVideos(ctx context.Context, playlistID string, maxPages int) ([]PlaylistEntry, error) {
	if !listIDRe.MatchString(playlistID) {
		return nil, fmt.Errorf("youtube playlist: %q is not a playlist id", playlistID)
	}
	if maxPages < 1 || maxPages > maxPlaylistPages {
		maxPages = maxPlaylistPages
	}
	resp, err := c.get(ctx, c.PlaylistBase+"?hl=en&list="+url.QueryEscape(playlistID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("youtube playlist %s: status %d", playlistID, resp.StatusCode)
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("youtube playlist %s read: %w", playlistID, err)
	}
	m := initialDataRe.FindSubmatch(page)
	if m == nil {
		return nil, fmt.Errorf("youtube playlist %s: page has no initial data", playlistID)
	}
	version := c.clientVersion()
	if v := clientVersionRe.FindSubmatch(page); v != nil {
		version = string(v[1])
		c.version.Store(version)
	}
	entries, token, err := parsePlaylistData(m[1])
	if err != nil {
		return nil, fmt.Errorf("youtube playlist %s: %w", playlistID, err)
	}
	seen := map[string]bool{}
	var out []PlaylistEntry
	add := func(es []PlaylistEntry) {
		for _, e := range es {
			if !seen[e.ID] {
				seen[e.ID] = true
				out = append(out, e)
			}
		}
	}
	add(entries)
	for pages := 1; token != "" && pages < maxPages; pages++ {
		next, nextToken, err := c.playlistContinuation(ctx, version, token)
		if err != nil {
			return out, fmt.Errorf("youtube playlist %s page %d: %w", playlistID, pages+1, err)
		}
		add(next)
		token = nextToken
	}
	return out, nil
}

func (c *Client) clientVersion() string {
	if v, ok := c.version.Load().(string); ok && v != "" {
		return v
	}
	return fallbackVersion
}

func (c *Client) playlistContinuation(ctx context.Context, version, token string) ([]PlaylistEntry, string, error) {
	body, err := json.Marshal(map[string]any{
		"context":      map[string]any{"client": map[string]string{"clientName": "WEB", "clientVersion": version, "hl": "en", "gl": "US"}},
		"continuation": token,
	})
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BrowseBase+"?prettyPrint=false", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("continuation request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("continuation status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, "", fmt.Errorf("continuation read: %w", err)
	}
	return parsePlaylistData(raw)
}

func parsePlaylistData(raw []byte) ([]PlaylistEntry, string, error) {
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, "", fmt.Errorf("bad playlist data: %w", err)
	}
	var out []PlaylistEntry
	token := ""
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if l, ok := x["lockupViewModel"].(map[string]any); ok {
				if e, ok := playlistLockup(l); ok {
					out = append(out, e)
				}
			}
			if cc, ok := x["continuationCommand"].(map[string]any); ok {
				if t, ok := cc["token"].(string); ok && t != "" {
					token = t
				}
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(data)
	return out, token, nil
}

func playlistLockup(l map[string]any) (PlaylistEntry, bool) {
	if t, _ := l["contentType"].(string); t != "LOCKUP_CONTENT_TYPE_VIDEO" {
		return PlaylistEntry{}, false
	}
	id, _ := l["contentId"].(string)
	if !ValidVideoID(id) {
		return PlaylistEntry{}, false
	}
	title := ""
	if md, ok := l["metadata"].(map[string]any); ok {
		if lm, ok := md["lockupMetadataViewModel"].(map[string]any); ok {
			if tt, ok := lm["title"].(map[string]any); ok {
				title, _ = tt["content"].(string)
			}
		}
	}
	return PlaylistEntry{ID: id, Title: title}, title != ""
}
