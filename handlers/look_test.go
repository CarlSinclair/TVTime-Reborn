package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShowSeasonAndEpisodePages(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	if code, out := alice.post("/api/show/555/follow", map[string]string{"status": "watching"}); code != 200 {
		t.Fatalf("follow: %d %v", code, out)
	}
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)

	_, page := alice.get("/show/555")
	if strings.Contains(page, "hero-bg") {
		t.Fatalf("a show with no artwork has no cover background")
	}
	if _, err := e.store.DB.Exec(`UPDATE shows SET backdrop_path = '/backdrop123.jpg' WHERE id = ?`, sh.ID); err != nil {
		t.Fatal(err)
	}
	_, page = alice.get("/show/555")
	if !strings.Contains(page, `class="hero-bg"`) || !strings.Contains(page, "/img/w1280/backdrop123.jpg") || !strings.Contains(page, "cover-hero") {
		t.Fatalf("the cover art is the background of the show page")
	}
	if strings.Contains(page, `class="poster"`) {
		t.Fatalf("the poster no longer takes up space beside the details")
	}

	_, season := alice.get("/show/555/season/1")
	if strings.Contains(season, "season-links pills") {
		t.Fatalf("a show with one season has no season switcher")
	}
	if !strings.Contains(season, "ep-cards") || !strings.Contains(season, "ep-art-empty") {
		t.Fatalf("episodes are cards, with a tile when there is no still")
	}

	_, ep := alice.get("/episode/" + itoa64(eps[0].ID))
	if strings.Contains(ep, `class="still"`) {
		t.Fatalf("an episode with no real still shows no placeholder image")
	}
	if strings.Contains(ep, "ep-ratings") {
		t.Fatalf("no ratings, no charts")
	}

	for i, ep := range eps {
		alice.post("/api/episode/"+itoa64(ep.ID)+"/rate", map[string]any{"rating": 6 + i*2})
	}
	_, season = alice.get("/show/555/season/1")
	if !strings.Contains(season, `<svg class="chart"`) {
		t.Fatalf("a season with ratings has a chart")
	}
	_, epPage := alice.get("/episode/" + itoa64(eps[0].ID))
	if !strings.Contains(epPage, `class="mark"`) || !strings.Contains(epPage, "star-dist") {
		t.Fatalf("an episode with ratings has a chart with itself marked, and a star breakdown")
	}
}

func TestStillCanBeChosenWithAStringEpisodeId(t *testing.T) {
	e := newEnv(t)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/original/stillAAAA1111.jpg" {
			w.Write(pngBytes(400, 225))
			return
		}
		http.NotFound(w, r)
	}))
	defer cdn.Close()
	e.cfg.ImageBase = cdn.URL
	alice := e.signup(t, "alice", true)
	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	code, out := alice.post("/api/pics/still", map[string]any{"episode": itoa64(eps[1].ID), "file": "/stillAAAA1111.jpg"})
	if code != 200 || out["name"] == nil {
		t.Fatalf("the page sends the episode id as text, which must work: %d %v", code, out)
	}
}

func TestMyShowsDefaultsToWatching(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	alice.post("/api/show/555/follow", map[string]string{"status": "plan"})
	_, page := alice.get("/shows")
	if !strings.Contains(page, `href="/show/555"`) {
		t.Fatalf("with nothing in Watching the page falls back to everything")
	}
	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	_, page = alice.get("/shows")
	if !strings.Contains(page, `href="/shows?status=all"`) || !strings.Contains(page, `aria-current="page"`) {
		t.Fatalf("the watching filter is selected by default")
	}
	alice.post("/api/show/555/follow", map[string]string{"status": "completed"})
	_, page = alice.get("/shows?status=watching")
	if strings.Contains(page, `href="/show/555"`) {
		t.Fatalf("an explicit watching filter shows only watching")
	}
	_, page = alice.get("/shows?status=all")
	if !strings.Contains(page, `href="/show/555"`) {
		t.Fatalf("all still shows everything")
	}
}
