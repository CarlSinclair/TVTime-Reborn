package tmdb

import (
	"context"
	"fmt"
	"net/url"
)

// EpisodeStills lists the still images TMDB has for one episode, as file paths
// (for example "/abc123.jpg"), best first.
func (c *Client) EpisodeStills(ctx context.Context, showID int64, season, episode int) ([]string, error) {
	var out struct {
		Stills []struct {
			FilePath string `json:"file_path"`
		} `json:"stills"`
	}
	if err := c.get(ctx, fmt.Sprintf("/tv/%d/season/%d/episode/%d/images", showID, season, episode), url.Values{}, &out); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(out.Stills))
	for _, s := range out.Stills {
		if s.FilePath != "" {
			paths = append(paths, s.FilePath)
		}
	}
	return paths, nil
}
