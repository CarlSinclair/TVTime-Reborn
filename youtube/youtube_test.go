package youtube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleFeed = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns:media="http://search.yahoo.com/mrss/" xmlns="http://www.w3.org/2005/Atom">
 <yt:channelId>4QZ_LsYcvcq7qOsOhpAX4A</yt:channelId>
 <title>ColdFusion</title>
 <entry>
  <yt:videoId>FsEbLPqeo7k</yt:videoId>
  <title>The Man Who Refused</title>
  <published>2026-11-11T13:49:23+00:00</published>
  <media:group><media:description>Line one</media:description></media:group>
 </entry>
 <entry>
  <yt:videoId>bad</yt:videoId>
  <title>Skipped</title>
  <published>2026-11-10T13:49:23+00:00</published>
 </entry>
</feed>`

func TestParseFeed(t *testing.T) {
	f, err := parseFeed("UC4QZ_LsYcvcq7qOsOhpAX4A", []byte(sampleFeed))
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "ColdFusion" || len(f.Videos) != 1 {
		t.Fatalf("feed = %+v", f)
	}
	v := f.Videos[0]
	if v.ID != "FsEbLPqeo7k" || v.Description != "Line one" || v.Published.Format("2006-01-02 15:04") != "2026-11-11 13:49" {
		t.Fatalf("video = %+v", v)
	}
	if _, err := parseFeed("UC4QZ_LsYcvcq7qOsOhpAX4A", []byte("<nope")); err == nil {
		t.Fatal("bad XML must be an error")
	}
}

func TestParseSearch(t *testing.T) {
	page := `<html><script>var ytInitialData = {"a":[{"channelRenderer":{"channelId":"UCVYamHliCI9rw1tHR1xbkfw","title":{"simpleText":"Dave2D"},` +
		`"thumbnail":{"thumbnails":[{"url":"//yt3.ggpht.com/small"},{"url":"//yt3.ggpht.com/big"}]},` +
		`"subscriberCountText":{"simpleText":"@Dave2D"},"videoCountText":{"simpleText":"3.69M subscribers"},` +
		`"descriptionSnippet":{"runs":[{"text":"Hey "},{"text":"everyone"}]}}}]};</script></html>`
	chs, err := parseSearch("dave2d", []byte(page))
	if err != nil || len(chs) != 1 {
		t.Fatalf("channels = %v err=%v", chs, err)
	}
	c := chs[0]
	if c.Title != "Dave2D" || c.Subscribers != 3690000 || c.Avatar != "https://yt3.ggpht.com/big" || c.Description != "Hey everyone" {
		t.Fatalf("channel = %+v", c)
	}
	if _, err := parseSearch("x", []byte("<html></html>")); err == nil {
		t.Fatal("a page without data must be an error")
	}
}

func TestParseSubscribers(t *testing.T) {
	for in, want := range map[string]int64{"5 subscribers": 5, "15K subscribers": 15000, "1.2M subscribers": 1200000, "1,234 subscribers": 1234} {
		if got, ok := parseSubscribers(in); !ok || got != want {
			t.Errorf("%q = %d %v, want %d", in, got, ok, want)
		}
	}
	if _, ok := parseSubscribers("@Dave2D"); ok {
		t.Error("a handle is not a count")
	}
}

func TestIsShort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/AAAAAAAAAAA"):
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/BBBBBBBBBBB"):
			http.Redirect(w, r, "/watch?v=BBBBBBBBBBB", http.StatusSeeOther)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	c := New()
	c.WatchBase = srv.URL
	ctx := context.Background()
	if short, err := c.IsShort(ctx, "AAAAAAAAAAA"); err != nil || !short {
		t.Fatalf("short = %v err=%v", short, err)
	}
	if short, err := c.IsShort(ctx, "BBBBBBBBBBB"); err != nil || short {
		t.Fatalf("video = %v err=%v", short, err)
	}
	if _, err := c.IsShort(ctx, "CCCCCCCCCCC"); err == nil {
		t.Fatal("a server error must not be read as a regular video")
	}
	if _, err := c.IsShort(ctx, "bad"); err == nil {
		t.Fatal("an invalid id must be an error")
	}
}

func TestParseSearchLockups(t *testing.T) {
	page := `<script>var ytInitialData = {"x":[` +
		`{"lockupViewModel":{"contentId":"PLHovnlOusNLhr7ttEReJUuC3QPHoohqZn","contentType":"LOCKUP_CONTENT_TYPE_SHOW",` +
		`"contentImage":{"collectionThumbnailViewModel":{"primaryThumbnail":{"thumbnailViewModel":{"overlays":[{"thumbnailOverlayBadgeViewModel":{"thumbnailBadges":[{"thumbnailBadgeViewModel":{"text":"2 episodes"}}]}}]}}}},` +
		`"metadata":{"lockupMetadataViewModel":{"title":{"content":"Knights of Guinevere"},"metadata":{"contentMetadataViewModel":{"metadataRows":[` +
		`{"metadataParts":[{"text":{"content":"GLITCH"}}],"lockupContentMetadataRowExtension":{"contentType":"METADATA_ROW_CONTENT_TYPE_BYLINE"}},` +
		`{"metadataParts":[{"text":{"content":"A thriller"}}],"lockupContentMetadataRowExtension":{"contentType":"METADATA_ROW_CONTENT_TYPE_DESCRIPTION"}}]}}}}}},` +
		`{"lockupViewModel":{"contentId":"MCAdbUaMlAE","contentType":"LOCKUP_CONTENT_TYPE_VIDEO"}}]};</script>`
	got, err := parseSearch("knights", []byte(page))
	if err != nil || len(got) != 1 {
		t.Fatalf("sources = %v err=%v", got, err)
	}
	s := got[0]
	if !s.Playlist() || s.Title != "Knights of Guinevere" || s.Owner != "GLITCH" || s.Description != "A thriller" || s.Videos != 2 {
		t.Fatalf("source = %+v", s)
	}
}

func TestFeedUsesPlaylistParameter(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(sampleFeed))
	}))
	defer srv.Close()
	c := New()
	c.FeedBase = srv.URL
	if _, err := c.Feed(context.Background(), "PLHovnlOusNLhr7ttEReJUuC3QPHoohqZn"); err != nil || query != "playlist_id=PLHovnlOusNLhr7ttEReJUuC3QPHoohqZn" {
		t.Fatalf("query=%q err=%v", query, err)
	}
	if _, err := c.Feed(context.Background(), "UC4QZ_LsYcvcq7qOsOhpAX4A"); err != nil || query != "channel_id=UC4QZ_LsYcvcq7qOsOhpAX4A" {
		t.Fatalf("query=%q err=%v", query, err)
	}
	if _, err := c.Feed(context.Background(), "nope"); err == nil {
		t.Fatal("an invalid id must be an error")
	}
}

func TestPlaylistVideosFollowsContinuation(t *testing.T) {
	lockup := func(id, title string) string {
		return `{"lockupViewModel":{"contentId":"` + id + `","contentType":"LOCKUP_CONTENT_TYPE_VIDEO","metadata":{"lockupMetadataViewModel":{"title":{"content":"` + title + `"}}}}}`
	}
	first := `<script>var ytInitialData = {"c":[` + lockup("AAAAAAAAAAA", "One") + `,` + lockup("BBBBBBBBBBB", "Two") +
		`,{"lockupViewModel":{"contentId":"PLHovnlOusNLhr7ttEReJUuC3QPHoohqZn","contentType":"LOCKUP_CONTENT_TYPE_PLAYLIST"}},` +
		`{"continuationCommand":{"token":"next-1"}}]};</script>"clientVersion":"2.20260102.03.04"`
	second := `{"onResponseReceivedActions":[` + lockup("CCCCCCCCCCC", "Three") + `,` + lockup("BBBBBBBBBBB", "Two") + `]}`
	var posted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b := make([]byte, 2048)
			n, _ := r.Body.Read(b)
			posted = string(b[:n])
			_, _ = w.Write([]byte(second))
			return
		}
		_, _ = w.Write([]byte(first))
	}))
	defer srv.Close()
	c := New()
	c.PlaylistBase = srv.URL
	c.BrowseBase = srv.URL
	got, err := c.PlaylistVideos(context.Background(), "PLHovnlOusNLhr7ttEReJUuC3QPHoohqZn", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "AAAAAAAAAAA" || got[2].ID != "CCCCCCCCCCC" || got[2].Title != "Three" {
		t.Fatalf("entries = %+v", got)
	}
	if !strings.Contains(posted, `"continuation":"next-1"`) || !strings.Contains(posted, "2.20260102.03.04") {
		t.Fatalf("continuation request = %s", posted)
	}
	if _, err := c.PlaylistVideos(context.Background(), "UC4QZ_LsYcvcq7qOsOhpAX4A", 5); err == nil {
		t.Fatal("a channel id is not a playlist")
	}
}

func TestPublishedAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 1024)
		n, _ := r.Body.Read(b)
		if strings.Contains(string(b[:n]), "AAAAAAAAAAA") {
			_, _ = w.Write([]byte(`{"microformat":{"playerMicroformatRenderer":{"publishDate":"2026-11-11T05:49:23-08:00"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"microformat":{}}`))
	}))
	defer srv.Close()
	c := New()
	c.PlayerBase, c.WatchBase = srv.URL, srv.URL
	got, err := c.PublishedAt(context.Background(), "AAAAAAAAAAA")
	if err != nil || got.Format("2006-01-02 15:04:05") != "2026-11-11 13:49:23" {
		t.Fatalf("published = %v err=%v", got, err)
	}
	if _, err := c.PublishedAt(context.Background(), "BBBBBBBBBBB"); err == nil {
		t.Fatal("a reply and a page without a date must be an error")
	}
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"microformat":{}}`))
			return
		}
		_, _ = w.Write([]byte(`<meta itemprop="datePublished" content="2024-02-03T04:05:06-08:00">`))
	}))
	defer page.Close()
	c.PlayerBase, c.WatchBase = page.URL, page.URL
	if got, err := c.PublishedAt(context.Background(), "BBBBBBBBBBB"); err != nil || got.Format("2006-01-02 15:04:05") != "2024-02-03 12:05:06" {
		t.Fatalf("page fallback = %v err=%v", got, err)
	}
	if UploadsPlaylist("UC4QZ_LsYcvcq7qOsOhpAX4A") != "UU4QZ_LsYcvcq7qOsOhpAX4A" || UploadsPlaylist("nope") != "" {
		t.Fatal("uploads playlist id")
	}
}
