package handlers_test

import (
	"strconv"
	"strings"
	"testing"
)

func TestRatingMarksWatchedAndHomeCardsUpdateInPlace(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "starry", true)
	if code, out := c.post("/api/show/555/follow", map[string]string{"status": "watching"}); code != 200 {
		t.Fatalf("follow: %d %v", code, out)
	}
	sh, err := e.store.ShowByTMDB(555)
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.store.EpisodeByNumber(sh.ID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.store.EpisodeByNumber(sh.ID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}

	_, home := c.get("/")
	if !strings.Contains(home, "Pilot") || !strings.Contains(home, `class="star`) {
		t.Fatalf("home must list the next episode with rating stars")
	}
	if strings.Contains(home, "Trending") || strings.Contains(home, `class="stats"`) {
		t.Fatalf("home must not carry the stats cards or the trending list")
	}

	code, out := c.post("/api/episode/"+itoa64(first.ID)+"/rate", map[string]any{"rating": 8, "card": true})
	if code != 200 || out["watched"] != true {
		t.Fatalf("rating must mark the episode watched: %d %v", code, out)
	}
	next, _ := out["next_html"].(string)
	if !strings.Contains(next, "Second") || !strings.Contains(next, "upnext-item") {
		t.Fatalf("the card must come back showing the next episode: %q", next)
	}
	u := e.mustUser(t, "starry")
	p, _ := e.store.Progress(u.ID, sh.ID)
	if p.Watched != 1 {
		t.Fatalf("watched = %d, want 1", p.Watched)
	}

	code, out = c.post("/api/episode/"+itoa64(second.ID)+"/watch", map[string]any{"watched": true, "card": true})
	if code != 200 || out["next_html"] != "" {
		t.Fatalf("caught up must return no card: %d %v", code, out)
	}
	_, page := c.get("/episode/" + itoa64(first.ID))
	if !strings.Contains(page, `data-stars="4"`) || strings.Contains(page, "<select data-rate") {
		t.Fatalf("episode page must show 4 filled stars instead of a dropdown")
	}
}

func TestCalendarShowsUpcomingFirstAndHidesHistory(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "calen", true)
	_, page := c.get("/calendar")
	if !strings.Contains(page, "/calendar?past=1") {
		t.Fatalf("the calendar needs a button to the past releases")
	}
	if code, body := c.get("/calendar?past=1"); code != 200 || !strings.Contains(body, `href="/calendar"`) {
		t.Fatalf("past view: %d", code)
	}
}

func TestSettingsPagesUseModalsAndRealValues(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "modal", true)
	_, acc := c.get("/settings?tab=account")
	for _, want := range []string{`data-modal-open="m-email"`, `data-modal-open="m-password"`, `id="twofa-start"`, "<dialog", `class="panel danger"`} {
		if !strings.Contains(acc, want) {
			t.Errorf("account page misses %s", want)
		}
	}
	if strings.Contains(acc, "telegram-login") {
		t.Errorf("Telegram connecting lives on the notifications tab only")
	}
	_, prof := c.get("/settings?tab=profile")
	if strings.Contains(prof, "data-remove-media") {
		t.Errorf("remove buttons must not show when nothing is uploaded")
	}
	if !strings.Contains(prof, `name="display_name" maxlength="40" value="">`) || strings.Contains(prof, `placeholder="modal"`) {
		t.Errorf("an unset display name stays blank, with no placeholder and nothing copied from the username")
	}
	if !strings.Contains(prof, `id="profile-public"`) {
		t.Errorf("public profile must be a switch")
	}
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
