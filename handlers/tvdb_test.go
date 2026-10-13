package handlers_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func TestTVDBShowsAppearInSearchAndWork(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "tuber", true)

	code, body := c.get("/search?q=tube")
	if code != 200 || !strings.Contains(body, "Tube Channel") || !strings.Contains(body, "/show/-9001") {
		t.Fatalf("a TheTVDB-only show must be searchable: %d", code)
	}
	if code, body := c.get("/search?q=fake"); code != 200 || strings.Contains(body, "Tube Channel") {
		t.Fatalf("unrelated search must not include it: %d", code)
	}

	code, body = c.get("/show/-9001")
	if code != 200 || !strings.Contains(body, "Tube Channel") || !strings.Contains(body, "TheTVDB") {
		t.Fatalf("show page: %d (attribution required)", code)
	}
	if !strings.Contains(body, "/tvdbimg/") {
		t.Fatalf("artwork must come through our own proxy")
	}
	if code, out := c.post("/api/show/-9001/follow", map[string]string{"status": "watching"}); code != 200 {
		t.Fatalf("follow: %d %v", code, out)
	}
	sh, err := e.store.ShowByTMDB(-9001)
	if err != nil || sh.TVDBID != 9001 || sh.Networks != "YouTube" || sh.NumberOfEpisodes != 2 {
		t.Fatalf("stored show: %+v %v", sh, err)
	}
	if code, body := c.get("/show/-9001/season/2020"); code != 200 || !strings.Contains(body, "First video") {
		t.Fatalf("season page: %d", code)
	}
	if code, _ := c.get("/about"); code != 200 {
		t.Fatalf("about: %d", code)
	}
	_, about := c.get("/about")
	if !strings.Contains(about, "TheTVDB") {
		t.Fatalf("the About page must credit TheTVDB")
	}
}

func TestTVDBArtworkProxy(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "arty", true)
	good := base64.RawURLEncoding.EncodeToString([]byte("/banners/posters/abc.jpg"))
	resp, err := c.http.Get(c.base + "/tvdbimg/" + good)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/png") {
		t.Fatalf("artwork: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, bad := range []string{"/etc/passwd", "/banners/../secret.jpg", "https://evil.example/x.jpg", "/banners/x.exe"} {
		code, _ := c.get("/tvdbimg/" + base64.RawURLEncoding.EncodeToString([]byte(bad)))
		if code != 404 {
			t.Errorf("%q must be refused, got %d", bad, code)
		}
	}
}

const tvdbExport = `user_id,key,created_at,s_id,series_name,season_number,episode_number,is_followed,is_for_later,is_archived,episode_id
1,watch-episode-a,2020-02-01 10:00:00,9001,Tube Channel,2020,1,,,,71
1,watch-episode-b,2020-02-02 10:00:00,9001,Tube Channel,2020,2,,,,72
1,watch-episode-c,2020-02-03 10:00:00,9001,Tube Channel,2020,9,,,,79
1,user-series-x,2020-01-01 00:00:00,9001,Tube Channel,,,true,false,false,
`

func TestImportFallsBackToTVDB(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "youtuber", true)
	export := zipOf(t, map[string]string{"tracking-prod-records-v2.csv": tvdbExport})
	if code, out := c.upload("/api/import/tvtime", export); code != 200 {
		t.Fatalf("upload: %d %v", code, out)
	}
	j := c.waitImport()
	if j["status"] != "done" || j["matched"] != float64(1) || j["episodes_added"] != float64(2) || j["episodes_skipped"] != float64(1) {
		t.Fatalf("a show TMDB lacks must import through TheTVDB: %v", j)
	}
	if un, _ := j["unmatched"].([]any); len(un) != 0 {
		t.Fatalf("unmatched = %v", un)
	}
}
