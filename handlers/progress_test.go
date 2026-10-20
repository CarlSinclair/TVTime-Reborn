package handlers_test

import (
	"strings"
	"testing"
)

func TestSeasonCoversShowProgressBars(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	if code, out := alice.post("/api/show/555/follow", map[string]string{"status": "watching"}); code != 200 {
		t.Fatalf("follow: %d %v", code, out)
	}
	_, page := alice.get("/show/555")
	if !strings.Contains(page, "0 of 2 watched") || strings.Contains(page, "count-badge") || strings.Contains(page, `progress class="done"`) {
		t.Fatalf("an unstarted season shows an empty bar with the count below the cover")
	}
	sh, err := e.store.ShowByTMDB(555)
	if err != nil {
		t.Fatal(err)
	}
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	u := e.mustUser(t, "alice")
	if err := e.store.MarkWatched(u.ID, eps[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	_, page = alice.get("/show/555")
	if !strings.Contains(page, "1 of 2 watched") || strings.Contains(page, `progress class="done"`) {
		t.Fatalf("a half-watched season is not green")
	}
	if err := e.store.MarkWatched(u.ID, eps[1].ID, ""); err != nil {
		t.Fatal(err)
	}
	_, page = alice.get("/show/555")
	if strings.Count(page, `<progress class="done"`) != 2 {
		t.Fatalf("a finished season and show are green (one bar each)")
	}
}

func TestFinishedEndedShowsMoveToCompleted(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	if code, out := alice.post("/api/show/555/follow", map[string]string{"status": "watching"}); code != 200 {
		t.Fatalf("follow: %d %v", code, out)
	}
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	u := e.mustUser(t, "alice")
	for _, ep := range eps {
		if code, out := alice.post("/api/episode/"+itoa64(ep.ID)+"/watch", map[string]any{"watched": true}); code != 200 {
			t.Fatalf("watch: %d %v", code, out)
		}
	}
	f, _ := e.store.FollowOf(u.ID, sh.ID)
	if f.Status != "watching" {
		t.Fatalf("a show that is still airing stays under watching, got %s", f.Status)
	}
	if _, err := e.store.DB.Exec(`UPDATE shows SET status = 'Ended' WHERE id = ?`, sh.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := e.store.CompleteFinished(0, 0); err != nil || n != 1 {
		t.Fatalf("CompleteFinished = %d, %v", n, err)
	}
	if f, _ = e.store.FollowOf(u.ID, sh.ID); f.Status != "completed" {
		t.Fatalf("status = %s", f.Status)
	}
	_, list := alice.get("/shows?status=completed")
	if !strings.Contains(list, "Fake Show") {
		t.Fatalf("the finished show belongs under the completed filter")
	}
	_, list = alice.get("/shows?status=watching")
	if strings.Contains(list, "Fake Show") {
		t.Fatalf("and no longer under watching")
	}
}

func TestProfilePostsSectionHiddenUntilThereArePosts(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)
	_, own := alice.get("/u/alice")
	if strings.Contains(own, `id="comments" class="comments" hidden`) || !strings.Contains(own, `id="comment-form"`) {
		t.Fatalf("the owner always sees the posts section with the form")
	}
	_, other := bob.get("/u/alice")
	if other != "" && strings.Contains(other, `id="comments"`) && !strings.Contains(other, `id="comments" class="comments" hidden`) {
		t.Fatalf("visitors get the posts section hidden until the page confirms there are posts")
	}
}
