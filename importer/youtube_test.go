package importer

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

const takeoutSample = `[
 {"header":"YouTube","title":"Watched One","titleUrl":"https://www.youtube.com/watch?v=AAAAAAAAAAA","subtitles":[{"name":"Chan","url":"https://www.youtube.com/channel/UCaaaaaaaaaaaaaaaaaaaaaa"}],"time":"2026-11-10T10:00:00.000Z","products":["YouTube"]},
 {"header":"YouTube","title":"Watched One again","titleUrl":"https://www.youtube.com/watch?v=AAAAAAAAAAA","subtitles":[{"name":"Chan"}],"time":"2026-11-01T10:00:00.000Z"},
 {"header":"YouTube","title":"Searched for x","titleUrl":"https://www.youtube.com/results?search_query=x","time":"2026-11-09T10:00:00.000Z"},
 {"header":"YouTube","title":"Watched a post","time":"2026-11-09T10:00:00.000Z"},
 {"header":"YouTube Music","title":"Watched Two","titleUrl":"https://music.youtube.com/watch?v=BBBBBBBBBBB&list=RDx","time":"2026-11-08T10:00:00Z"},
 {"header":"YouTube","title":"Watched Three","titleUrl":"https://youtu.be/CCCCCCCCCCC","time":"bad"}
]`

func TestParseYouTubeTakeoutJSON(t *testing.T) {
	data, err := ParseYouTubeTakeout(strings.NewReader(takeoutSample), int64(len(takeoutSample)))
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Videos) != 3 {
		t.Fatalf("videos = %+v", data.Videos)
	}
	one := data.Videos[0]
	if one.ID != "AAAAAAAAAAA" || one.Channel != "Chan" || one.At != "2026-11-01 10:00:00" {
		t.Fatalf("a repeated watch must keep the first time: %+v", one)
	}
	if one.Title != "One" || one.ChannelID != "UCaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("title and channel id: %+v", one)
	}
	if data.Videos[1].ID != "BBBBBBBBBBB" || data.Videos[2].ID != "CCCCCCCCCCC" || data.Videos[2].At != "" {
		t.Fatalf("videos = %+v", data.Videos)
	}
}

func TestParseYouTubeTakeoutZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("Takeout/YouTube e YouTube Music/histórico/histórico-de-exibição.json")
	_, _ = w.Write([]byte(takeoutSample))
	w, _ = zw.Create("Takeout/YouTube e YouTube Music/histórico/histórico-de-pesquisa.json")
	_, _ = w.Write([]byte(`[{"titleUrl":"https://www.youtube.com/results?search_query=q"}]`))
	w, _ = zw.Create("Takeout/archive_browser.html")
	_, _ = w.Write([]byte("<html></html>"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := ParseYouTubeTakeout(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil || len(data.Videos) != 3 {
		t.Fatalf("videos = %+v err=%v", data, err)
	}
}

func TestParseYouTubeTakeoutRejects(t *testing.T) {
	html := "<html>watch-history</html>"
	if _, err := ParseYouTubeTakeout(strings.NewReader(html), int64(len(html))); !errors.Is(err, ErrNotYouTube) {
		t.Fatalf("html must be rejected, got %v", err)
	}
	empty := `[{"titleUrl":"https://www.youtube.com/results?search_query=q"}]`
	if _, err := ParseYouTubeTakeout(strings.NewReader(empty), int64(len(empty))); !errors.Is(err, ErrEmpty) {
		t.Fatalf("a history without watches must be ErrEmpty, got %v", err)
	}
}
