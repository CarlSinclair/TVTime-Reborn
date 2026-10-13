package handlers_test

import (
	"encoding/json"
	"strings"
	"testing"
)

func (c *client) getJSON(path string) map[string]any {
	_, body := c.get(path)
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		c.t.Fatalf("GET %s: not JSON: %q", path, body)
	}
	return out
}

func TestPeopleSearchIsExactAndHidesBlockedAccounts(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)

	for _, q := range []string{"bobby", "@bobby", "bobby@here", "BOBBY"} {
		out := alice.getJSON("/api/people?q=" + q)
		res, _ := out["results"].([]any)
		if len(res) != 1 {
			t.Errorf("%q should find bobby exactly, got %v", q, out)
		}
	}
	for _, q := range []string{"bob", "bobb", "ali", "obby"} {
		out := alice.getJSON("/api/people?q=" + q)
		if res, _ := out["results"].([]any); len(res) != 0 {
			t.Errorf("%q must not match by prefix or fragment: %v", q, out)
		}
	}
	if out := alice.getJSON("/api/people?q=bobby@other.example"); out["remote"] != true {
		t.Errorf("another server's handle must be flagged: %v", out)
	}

	if code, _ := alice.post("/api/blocks/unblock", map[string]string{"username": "bobby"}); code != 200 {
		t.Logf("unblock noop: %d", code)
	}
	bobUser := e.mustUser(t, "bobby")
	aliceUser := e.mustUser(t, "alice")
	if err := e.store.Block(aliceUser.ID, "bobby"); err != nil {
		t.Fatal(err)
	}
	if out := bob.getJSON("/api/people?q=alice"); len(out["results"].([]any)) != 0 {
		t.Fatalf("a blocked person must not find the blocker: %v", out)
	}
	if code, _ := bob.get("/u/alice"); code != 404 {
		t.Fatalf("a blocked person must see no profile: %d", code)
	}
	if code, _ := bob.post("/api/friends/request", map[string]string{"username": "alice"}); code != 404 {
		t.Fatalf("a blocked person cannot send a request: %d", code)
	}
	_ = bobUser
}

func TestFriendRequestsAndNotifications(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)

	_, page := alice.get("/u/bobby")
	if !strings.Contains(page, `data-friend-add="bobby"`) {
		t.Fatalf("a profile viewed by someone else needs the add button")
	}
	_, own := alice.get("/u/alice")
	if strings.Contains(own, "data-friend-add") {
		t.Fatalf("your own profile has no add button")
	}
	if code, out := alice.post("/api/friends/request", map[string]string{"username": "bobby"}); code != 200 || out["state"] != "outgoing" {
		t.Fatalf("request: %d %v", code, out)
	}
	if code, _ := alice.post("/api/friends/request", map[string]string{"username": "alice"}); code != 400 {
		t.Fatalf("befriending yourself must fail: %d", code)
	}
	list := bob.getJSON("/api/friends")
	if in, _ := list["incoming"].([]any); len(in) != 1 {
		t.Fatalf("incoming = %v", list)
	}
	bu := e.mustUser(t, "bobby")
	notes, _ := e.store.Notifications(bu.ID, 10, 0)
	if len(notes) == 0 || notes[0].Event != "friend_request" {
		t.Fatalf("the request must notify the other person: %+v", notes)
	}
	if code, out := bob.post("/api/friends/respond", map[string]any{"username": "alice", "accept": true}); code != 200 || out["state"] != "friends" {
		t.Fatalf("accept: %d %v", code, out)
	}
	if fl := alice.getJSON("/api/friends"); len(fl["friends"].([]any)) != 1 {
		t.Fatalf("alice should now have a friend: %v", fl)
	}
	if code, _ := alice.post("/api/friends/remove", map[string]string{"username": "bobby"}); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	if fl := bob.getJSON("/api/friends"); len(fl["friends"].([]any)) != 0 {
		t.Fatalf("friendship must end for both: %v", fl)
	}
}

func TestFeedShowsOnlyFriendsAndHidesSpoilers(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)
	carol := e.signup(t, "carol", true)

	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh, _ := e.store.ShowByTMDB(555)
	ep1, _ := e.store.EpisodeByNumber(sh.ID, 1, 1)
	ep2, _ := e.store.EpisodeByNumber(sh.ID, 1, 2)

	bob.post("/api/episode/"+itoa64(ep1.ID)+"/watch", map[string]any{"watched": true})
	bob.post("/api/episode/"+itoa64(ep1.ID)+"/rate", map[string]any{"rating": 8})
	bob.post("/api/episode/"+itoa64(ep1.ID)+"/comments", map[string]any{"body": "Great pilot, loved it"})
	bob.post("/api/episode/"+itoa64(ep2.ID)+"/comments", map[string]any{"body": "The twist is unbelievable", "spoiler": true})
	carol.post("/api/episode/"+itoa64(ep1.ID)+"/comments", map[string]any{"body": "Carol is not a friend"})

	feed := alice.getJSON("/api/feed")
	if feed["mode"] != "suggested" {
		t.Fatalf("with no friends the feed must offer open comments: %v", feed["mode"])
	}
	html, _ := feed["html"].(string)
	if strings.Contains(html, "unbelievable") {
		t.Fatalf("a spoiler comment must never appear")
	}
	if !strings.Contains(html, "Great pilot") || !strings.Contains(html, "Carol is not a friend") {
		t.Fatalf("open comments from anyone are the fallback: %q", html)
	}

	alice.post("/api/friends/request", map[string]string{"username": "bobby"})
	bob.post("/api/friends/respond", map[string]any{"username": "alice", "accept": true})
	feed = alice.getJSON("/api/feed")
	html, _ = feed["html"].(string)
	if feed["mode"] != "friends" {
		t.Fatalf("mode = %v", feed["mode"])
	}
	for _, want := range []string{"bobby", "Great pilot", "Fake Show"} {
		if !strings.Contains(html, want) {
			t.Errorf("friend feed misses %q", want)
		}
	}
	if strings.Contains(html, "unbelievable") || strings.Contains(html, "Carol is not a friend") {
		t.Fatalf("spoilers and non-friends must not appear: %q", html)
	}
	if strings.Contains(html, "S01E01 of") && !strings.Contains(html, "commented on") {
		t.Fatalf("watch and rating lines must not name an episode the viewer has not seen")
	}
	if !strings.Contains(html, "rated an episode of") || !strings.Contains(html, "watched an episode of") {
		t.Fatalf("watch and rating events must appear without naming the episode: %q", html)
	}

	alice.post("/api/episode/"+itoa64(ep1.ID)+"/watch", map[string]any{"watched": true})
	html, _ = alice.getJSON("/api/feed")["html"].(string)
	if !strings.Contains(html, "watched S01E01 of") || !strings.Contains(html, "rated S01E01 of") {
		t.Fatalf("once the viewer has seen it the episode is named: %q", html)
	}

	bu := e.mustUser(t, "bobby")
	if _, err := e.store.DB.Exec(`UPDATE users SET share_activity = 0 WHERE id = ?`, bu.ID); err != nil {
		t.Fatal(err)
	}
	html, _ = alice.getJSON("/api/feed")["html"].(string)
	if strings.Contains(html, "bobby") {
		t.Fatalf("someone who stopped sharing must disappear from the feed")
	}
}

func TestHomeHasUpcomingAndFeedTabs(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "tabby", true)
	_, home := c.get("/")
	for _, want := range []string{`data-home-tab="upcoming"`, `data-home-tab="feed"`, `id="feed-list"`, "data-friends-open"} {
		if !strings.Contains(home, want) {
			t.Errorf("home misses %s", want)
		}
	}
	if code, body := c.get("/search?kind=people&q=nobody"); code != 200 || !strings.Contains(body, "No one with that username") {
		t.Fatalf("people search page: %d", code)
	}
}

func TestProfileActionsAndNotificationButtons(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)

	_, page := alice.get("/u/bobby")
	for _, want := range []string{`data-friend-add="bobby"`, `data-block-user="bobby"`, `data-report-user="bobby"`} {
		if !strings.Contains(page, want) {
			t.Errorf("another person's profile misses %s", want)
		}
	}
	if strings.Contains(page, ">Add friend<") {
		t.Errorf("the profile buttons must be icon only")
	}
	alice.post("/api/friends/request", map[string]string{"username": "bobby"})

	_, notes := bob.get("/notifications")
	if !strings.Contains(notes, `data-friend-accept="alice"`) || !strings.Contains(notes, `data-friend-decline="alice"`) {
		t.Fatalf("the request notification must carry accept and decline buttons")
	}
	recent := bob.getJSON("/api/notifications/recent")
	items, _ := recent["items"].([]any)
	if len(items) == 0 || items[0].(map[string]any)["friend"] != "alice" {
		t.Fatalf("the bell data must name the requester: %v", recent)
	}
	bob.post("/api/friends/respond", map[string]any{"username": "alice", "accept": true})
	_, notes = bob.get("/notifications")
	if strings.Contains(notes, "data-friend-accept") {
		t.Fatalf("the buttons must disappear once answered")
	}
	_, page = alice.get("/u/bobby")
	if !strings.Contains(page, `data-friend-remove="bobby"`) || strings.Contains(page, `data-friend-add="bobby"`) {
		t.Fatalf("friends see a remove button instead of add")
	}

	if code, _ := alice.post("/api/users/report", map[string]string{"username": "bobby", "reason": "spam"}); code != 200 {
		t.Fatalf("report: %d", code)
	}
	if code, _ := alice.post("/api/users/report", map[string]string{"username": "bobby", "reason": "again"}); code != 409 {
		t.Fatalf("a second report must be refused: %d", code)
	}
	if code, _ := alice.post("/api/users/block", map[string]string{"username": "bobby"}); code != 200 {
		t.Fatalf("block: %d", code)
	}
	if code, _ := bob.get("/u/alice"); code != 404 {
		t.Fatalf("a blocked person sees nothing: %d", code)
	}
	if fl := alice.getJSON("/api/friends"); len(fl["friends"].([]any)) != 0 {
		t.Fatalf("blocking must end the friendship: %v", fl)
	}
}

func TestProfilePostsReuseComments(t *testing.T) {
	e := newEnv(t)
	owner := e.signup(t, "creator", true)
	fan := e.signup(t, "fanfan", true)
	stranger := e.signup(t, "outsider", true)
	ou := e.mustUser(t, "creator")
	path := "/api/profile/" + itoa64(ou.ID) + "/comments"

	if code, _ := fan.post(path, map[string]any{"body": "my own top level post"}); code != 403 {
		t.Fatalf("only the owner starts a thread on their profile: %d", code)
	}
	code, out := owner.post(path, map[string]any{"body": "New episode drops friday"})
	if code != 200 {
		t.Fatalf("owner post: %d %v", code, out)
	}
	pid := int64(out["id"].(float64))
	if code, _ := fan.post(path, map[string]any{"body": "Can't wait", "parent_id": pid}); code != 200 {
		t.Fatalf("anyone who can see the profile may reply: %d", code)
	}
	list := stranger.getJSON(path)
	if int(list["total"].(float64)) != 2 || len(list["comments"].([]any)) != 2 {
		t.Fatalf("thread = %v", list)
	}

	if code, _ := fan.post("/api/comments/"+itoa64(pid)+"/report", map[string]string{"kind": "abuse", "reason": "test"}); code != 200 {
		t.Fatalf("posts are reported like any comment: %d", code)
	}

	fan.post("/api/friends/request", map[string]string{"username": "creator"})
	owner.post("/api/friends/respond", map[string]any{"username": "fanfan", "accept": true})
	owner.post(path, map[string]any{"body": "A spoiler about the finale", "spoiler": true})
	html, _ := fan.getJSON("/api/feed")["html"].(string)
	if !strings.Contains(html, "New episode drops friday") || strings.Contains(html, "finale") {
		t.Fatalf("a friend's open posts appear in the feed, spoilers do not: %q", html)
	}

	if _, err := e.store.DB.Exec(`UPDATE users SET profile_public = 0 WHERE id = ?`, ou.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := stranger.get(path); code != 404 {
		t.Fatalf("a private profile's thread is not visible to strangers: %d", code)
	}
	if code, _ := fan.get(path); code != 200 {
		t.Fatalf("friends still see it: %d", code)
	}
	_, prof := owner.get("/u/creator")
	if !strings.Contains(prof, `data-kind="profile"`) || !strings.Contains(prof, `id="comment-form"`) {
		t.Fatalf("the owner's profile shows the posts section with a form")
	}
	_, prof = fan.get("/u/creator")
	if !strings.Contains(prof, `data-kind="profile"`) || strings.Contains(prof, `id="comment-form"`) {
		t.Fatalf("visitors see posts but no top-level form")
	}
}

func TestOnlyOnePendingRequestUntilAnswered(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)
	bu := e.mustUser(t, "bobby")

	for i := 0; i < 3; i++ {
		if code, out := alice.post("/api/friends/request", map[string]string{"username": "bobby"}); code != 200 || out["state"] != "outgoing" {
			t.Fatalf("request %d: %d %v", i, code, out)
		}
	}
	notes, _ := e.store.Notifications(bu.ID, 20, 0)
	n := 0
	for _, x := range notes {
		if x.Event == "friend_request" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("an unanswered request can be sent once; bob got %d notifications", n)
	}
	if in := bob.getJSON("/api/friends")["incoming"].([]any); len(in) != 1 {
		t.Fatalf("incoming = %v", in)
	}

	bob.post("/api/friends/respond", map[string]any{"username": "alice", "accept": false})
	if code, out := alice.post("/api/friends/request", map[string]string{"username": "bobby"}); code != 200 || out["state"] != "outgoing" {
		t.Fatalf("after a decline a new request may be sent: %d %v", code, out)
	}
	notes, _ = e.store.Notifications(bu.ID, 20, 0)
	n = 0
	for _, x := range notes {
		if x.Event == "friend_request" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("an identical notification is not repeated within minutes, so a decline-and-ask-again loop cannot spam: %d", n)
	}
	if in := bob.getJSON("/api/friends")["incoming"].([]any); len(in) != 1 {
		t.Fatalf("the new request must still be waiting in the list: %v", in)
	}
}
