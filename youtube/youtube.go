// Package youtube reads public YouTube data without the Data API: the Atom
// upload feed of a channel or playlist, the channel and playlist search pages,
// and whether an upload is a Short. Everything here is best effort scraping of
// public pages, so callers must treat every error as retryable and never as
// proof that a video, channel or playlist is gone.
package youtube

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var (
	ErrNotFound   = errors.New("youtube: not found")
	channelIDRe   = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	playlistIDRe  = regexp.MustCompile(`^PL[A-Za-z0-9_-]{10,40}$`)
	listIDRe      = regexp.MustCompile(`^(?:PL|UU)[A-Za-z0-9_-]{10,40}$`)
	videoIDRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	initialDataRe = regexp.MustCompile(`(?s)ytInitialData\s*=\s*(\{.*?\});\s*</script>`)
	subsRe        = regexp.MustCompile(`(?i)^([0-9][0-9.,]*)\s*([KMB]?)\s+subscribers?$`)
	countRe       = regexp.MustCompile(`^([0-9][0-9,]*)\s+(?:videos?|episodes?)$`)
)

const (
	maxBody          = 4 << 20
	channelFilter    = "EgIQAg%253D%253D"
	playlistFilter   = "EgIQAw%253D%253D"
	lockupPlaylist   = "LOCKUP_CONTENT_TYPE_PLAYLIST"
	lockupShow       = "LOCKUP_CONTENT_TYPE_SHOW"
	maxSearchPerKind = 10
)

type Video struct {
	ID          string
	Title       string
	Description string
	Published   time.Time
}

type Feed struct {
	SourceID string
	Title    string
	Videos   []Video
}

// Source is a channel or a playlist found by search. Videos is the count the
// search page shows (0 when it shows none) and Subscribers only means
// something for a channel.
type Source struct {
	ID          string
	Title       string
	Owner       string
	Description string
	Avatar      string
	Subscribers int64
	Videos      int
}

func (s Source) Playlist() bool { return IsPlaylist(s.ID) }

type Client struct {
	version      atomic.Value
	HTTP         *http.Client
	FeedBase     string
	SearchBase   string
	WatchBase    string
	PlayerBase   string
	PlaylistBase string
	BrowseBase   string
}

func New() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout:       20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		FeedBase:     "https://www.youtube.com/feeds/videos.xml",
		SearchBase:   "https://www.youtube.com/results",
		WatchBase:    "https://www.youtube.com",
		PlayerBase:   "https://www.youtube.com/youtubei/v1/player",
		PlaylistBase: "https://www.youtube.com/playlist",
		BrowseBase:   "https://www.youtube.com/youtubei/v1/browse",
	}
}

func ValidChannelID(id string) bool { return channelIDRe.MatchString(id) }

func IsPlaylist(id string) bool { return playlistIDRe.MatchString(id) }

// ValidSourceID reports whether id names a channel or a playlist.
func ValidSourceID(id string) bool {
	return channelIDRe.MatchString(id) || playlistIDRe.MatchString(id)
}

func ValidVideoID(id string) bool { return videoIDRe.MatchString(id) }

func (c *Client) get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("youtube request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cookie", "CONSENT=YES+1; SOCS=CAI")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("youtube GET %s: %w", rawURL, err)
	}
	return resp, nil
}

type atomFeed struct {
	Title   string `xml:"title"`
	Entries []struct {
		VideoID   string `xml:"videoId"`
		Title     string `xml:"title"`
		Published string `xml:"published"`
		Group     struct {
			Description string `xml:"description"`
		} `xml:"group"`
	} `xml:"entry"`
}

// Feed returns the recent uploads of a channel or playlist (a feed holds about
// 15 entries), in the order YouTube lists them.
func (c *Client) Feed(ctx context.Context, sourceID string) (*Feed, error) {
	param := "channel_id"
	switch {
	case IsPlaylist(sourceID):
		param = "playlist_id"
	case !ValidChannelID(sourceID):
		return nil, fmt.Errorf("youtube feed: invalid source id %q", sourceID)
	}
	resp, err := c.get(ctx, c.FeedBase+"?"+param+"="+url.QueryEscape(sourceID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("youtube feed source=%s: status %d", sourceID, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("youtube feed source=%s read: %w", sourceID, err)
	}
	return parseFeed(sourceID, body)
}

func parseFeed(sourceID string, body []byte) (*Feed, error) {
	var af atomFeed
	if err := xml.Unmarshal(body, &af); err != nil {
		return nil, fmt.Errorf("youtube feed source=%s: bad XML: %w", sourceID, err)
	}
	out := &Feed{SourceID: sourceID, Title: strings.TrimSpace(af.Title)}
	for _, e := range af.Entries {
		if !ValidVideoID(e.VideoID) {
			continue
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(e.Published))
		if err != nil {
			continue
		}
		out.Videos = append(out.Videos, Video{
			ID: e.VideoID, Title: strings.TrimSpace(e.Title), Description: strings.TrimSpace(e.Group.Description), Published: t.UTC(),
		})
	}
	if out.Title == "" {
		return nil, fmt.Errorf("youtube feed source=%s: no title in feed", sourceID)
	}
	return out, nil
}

// IsShort reports whether an upload is a YouTube Short: /shorts/<id> answers 200
// for a Short and redirects for a regular video.
func (c *Client) IsShort(ctx context.Context, videoID string) (bool, error) {
	if !ValidVideoID(videoID) {
		return false, fmt.Errorf("youtube short check: invalid video id %q", videoID)
	}
	resp, err := c.get(ctx, c.WatchBase+"/shorts/"+videoID)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode == http.StatusOK:
		return true, nil
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return false, nil
	}
	return false, fmt.Errorf("youtube short check video=%s: status %d", videoID, resp.StatusCode)
}

// Search returns the channels and the playlists matching q, each kind in
// YouTube's own relevance order. When one of the two searches fails the other's
// results are still returned together with the error.
func (c *Client) Search(ctx context.Context, q string) ([]Source, error) {
	var out []Source
	var errs []error
	for _, filter := range []string{channelFilter, playlistFilter} {
		found, err := c.search(ctx, q, filter)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(found) > maxSearchPerKind {
			found = found[:maxSearchPerKind]
		}
		out = append(out, found...)
	}
	return out, errors.Join(errs...)
}

func (c *Client) search(ctx context.Context, q, filter string) ([]Source, error) {
	resp, err := c.get(ctx, c.SearchBase+"?search_query="+url.QueryEscape(q)+"&sp="+filter)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("youtube search q=%q: status %d", q, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("youtube search q=%q read: %w", q, err)
	}
	return parseSearch(q, body)
}

type text struct {
	SimpleText string `json:"simpleText"`
	Runs       []struct {
		Text string `json:"text"`
	} `json:"runs"`
}

func (t text) String() string {
	if t.SimpleText != "" {
		return t.SimpleText
	}
	var b strings.Builder
	for _, r := range t.Runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

type channelRenderer struct {
	ChannelID string `json:"channelId"`
	Title     text   `json:"title"`
	Thumbnail struct {
		Thumbnails []struct {
			URL string `json:"url"`
		} `json:"thumbnails"`
	} `json:"thumbnail"`
	Description     text `json:"descriptionSnippet"`
	SubscriberCount text `json:"subscriberCountText"`
	VideoCount      text `json:"videoCountText"`
}

type lockup struct {
	ContentID    string `json:"contentId"`
	ContentType  string `json:"contentType"`
	ContentImage struct {
		Collection struct {
			Primary struct {
				Thumb struct {
					Overlays []struct {
						Badge struct {
							Badges []struct {
								Badge struct {
									Text string `json:"text"`
								} `json:"thumbnailBadgeViewModel"`
							} `json:"thumbnailBadges"`
						} `json:"thumbnailOverlayBadgeViewModel"`
					} `json:"overlays"`
				} `json:"thumbnailViewModel"`
			} `json:"primaryThumbnail"`
		} `json:"collectionThumbnailViewModel"`
	} `json:"contentImage"`
	Metadata struct {
		Lockup struct {
			Title struct {
				Content string `json:"content"`
			} `json:"title"`
			Metadata struct {
				Content struct {
					Rows []struct {
						Parts []struct {
							Text struct {
								Content string `json:"content"`
							} `json:"text"`
						} `json:"metadataParts"`
						Ext struct {
							Type string `json:"contentType"`
						} `json:"lockupContentMetadataRowExtension"`
					} `json:"metadataRows"`
				} `json:"contentMetadataViewModel"`
			} `json:"metadata"`
		} `json:"lockupMetadataViewModel"`
	} `json:"metadata"`
}

func parseSearch(q string, body []byte) ([]Source, error) {
	m := initialDataRe.FindSubmatch(body)
	if m == nil {
		return nil, fmt.Errorf("youtube search q=%q: page has no initial data", q)
	}
	var data any
	if err := json.Unmarshal(m[1], &data); err != nil {
		return nil, fmt.Errorf("youtube search q=%q: bad initial data: %w", q, err)
	}
	var out []Source
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if raw, ok := x["channelRenderer"]; ok {
				if s, ok := decodeChannel(raw); ok {
					out = append(out, s)
				}
			}
			if raw, ok := x["lockupViewModel"]; ok {
				if s, ok := decodeLockup(raw); ok {
					out = append(out, s)
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
	seen := map[string]bool{}
	dedup := out[:0]
	for _, s := range out {
		if seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		dedup = append(dedup, s)
	}
	return dedup, nil
}

func decodeChannel(raw any) (Source, bool) {
	b, err := json.Marshal(raw)
	if err != nil {
		return Source{}, false
	}
	var cr channelRenderer
	if err := json.Unmarshal(b, &cr); err != nil || !ValidChannelID(cr.ChannelID) {
		return Source{}, false
	}
	s := Source{ID: cr.ChannelID, Title: strings.TrimSpace(cr.Title.String()), Description: strings.TrimSpace(cr.Description.String())}
	if n := len(cr.Thumbnail.Thumbnails); n > 0 {
		u := cr.Thumbnail.Thumbnails[n-1].URL
		if strings.HasPrefix(u, "//") {
			u = "https:" + u
		}
		s.Avatar = u
	}
	for _, t := range []text{cr.SubscriberCount, cr.VideoCount} {
		if n, ok := parseSubscribers(t.String()); ok {
			s.Subscribers = n
		}
	}
	return s, s.Title != ""
}

func decodeLockup(raw any) (Source, bool) {
	b, err := json.Marshal(raw)
	if err != nil {
		return Source{}, false
	}
	var l lockup
	if err := json.Unmarshal(b, &l); err != nil {
		return Source{}, false
	}
	if (l.ContentType != lockupPlaylist && l.ContentType != lockupShow) || !IsPlaylist(l.ContentID) {
		return Source{}, false
	}
	s := Source{ID: l.ContentID, Title: strings.TrimSpace(l.Metadata.Lockup.Title.Content)}
	for _, row := range l.Metadata.Lockup.Metadata.Content.Rows {
		if len(row.Parts) == 0 {
			continue
		}
		switch row.Ext.Type {
		case "METADATA_ROW_CONTENT_TYPE_BYLINE":
			s.Owner = strings.TrimSpace(row.Parts[0].Text.Content)
		case "METADATA_ROW_CONTENT_TYPE_DESCRIPTION":
			s.Description = strings.TrimSpace(row.Parts[0].Text.Content)
		}
	}
	for _, o := range l.ContentImage.Collection.Primary.Thumb.Overlays {
		for _, bd := range o.Badge.Badges {
			if m := countRe.FindStringSubmatch(strings.TrimSpace(bd.Badge.Text)); m != nil {
				s.Videos, _ = strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
			}
		}
	}
	return s, s.Title != ""
}

// parseSubscribers reads "3.69M subscribers"; the search page puts the count
// in either of two fields depending on whether the channel has a handle.
func parseSubscribers(s string) (int64, bool) {
	m := subsRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToUpper(m[2]) {
	case "K":
		f *= 1e3
	case "M":
		f *= 1e6
	case "B":
		f *= 1e9
	}
	return int64(f), true
}

var avatarHosts = map[string]bool{"yt3.googleusercontent.com": true, "yt3.ggpht.com": true}

var avatarPathRe = regexp.MustCompile(`^/[A-Za-z0-9_/.=-]{4,300}$`)

// AvatarPath turns a channel avatar URL into the stored artwork path
// "ytc:<host+path, base64url>", or "" for a host that is not YouTube's.
func AvatarPath(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || !avatarHosts[u.Host] || !avatarPathRe.MatchString(u.Path) {
		return ""
	}
	return "ytc:" + base64.RawURLEncoding.EncodeToString([]byte(u.Host+u.Path))
}

// AvatarURL is the full-size URL for the encoded part of an AvatarPath.
func AvatarURL(encoded string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	host, path, ok := strings.Cut(string(raw), "/")
	if !ok || !avatarHosts[host] || !avatarPathRe.MatchString("/"+path) {
		return "", false
	}
	if i := strings.LastIndex(path, "=s"); i > 0 {
		path = path[:i]
	}
	return "https://" + host + "/" + path + "=s800-c-k-c0x00ffffff-no-rj", true
}

// ThumbnailURLs are the thumbnails of a video, best first.
func ThumbnailURLs(videoID string) []string {
	return []string{
		"https://i.ytimg.com/vi/" + videoID + "/maxresdefault.jpg",
		"https://i.ytimg.com/vi/" + videoID + "/hqdefault.jpg",
	}
}
