package handlers_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type remoteItem struct {
	ID       int64  `json:"id"`
	User     string `json:"user"`
	Host     string `json:"host"`
	Body     string `json:"body"`
	Spoiler  bool   `json:"spoiler"`
	ParentID int64  `json:"parent_id"`
}

type remoteList struct {
	Remote []remoteItem
}

func remoteOf(t *testing.T, c *client, epID int64) remoteList {
	t.Helper()
	_, raw := c.get("/api/episode/" + itoa64(epID) + "/comments")
	var all struct {
		Comments []remoteItem `json:"comments"`
	}
	if err := json.Unmarshal([]byte(raw), &all); err != nil {
		t.Fatalf("comments json: %v %s", err, raw)
	}
	var out remoteList
	for _, c := range all.Comments {
		if c.Host != "" {
			out.Remote = append(out.Remote, c)
		}
	}
	return out
}

func TestEpisodeCommentsReplicateToAllowListedServers(t *testing.T) {
	a, b := newEnv(t), newEnv(t)
	ctx := context.Background()
	alice := a.signup(t, "alice", true)
	bob := b.signup(t, "bobby", true)
	for _, c := range []*client{alice, bob} {
		c.post("/api/show/555/follow", map[string]string{"status": "watching"})
	}
	if _, err := a.fed.Connect(ctx, hostOf(b)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.fed.Connect(ctx, hostOf(a)); err != nil {
		t.Fatal(err)
	}
	shA, _ := a.store.ShowByTMDB(555)
	shB, _ := b.store.ShowByTMDB(555)
	epA, _ := a.store.EpisodeByNumber(shA.ID, 1, 1)
	epB, _ := b.store.EpisodeByNumber(shB.ID, 1, 1)
	listA := "/api/episode/" + itoa64(epA.ID) + "/comments"

	alice.post(listA, map[string]any{"body": "not allow-listed yet"})
	a.flush(t)
	if got := remoteOf(t, bob, epB.ID); len(got.Remote) != 0 {
		t.Fatalf("nothing is shared until a server is allow-listed: %+v", got)
	}

	if err := a.store.SetPeerShare(hostOf(b), true); err != nil {
		t.Fatal(err)
	}
	if err := b.store.SetPeerShare(hostOf(a), true); err != nil {
		t.Fatal(err)
	}
	if code, out := alice.post(listA, map[string]any{"body": "Spoiler: the butler did it", "spoiler": true}); code != 200 {
		t.Fatalf("comment: %d %v", code, out)
	}
	var top int64
	a.store.DB.QueryRow(`SELECT id FROM comments WHERE body LIKE 'Spoiler:%'`).Scan(&top)
	alice.post(listA, map[string]any{"body": "and a reply", "parent_id": top})
	a.flush(t)
	got := remoteOf(t, bob, epB.ID)
	if len(got.Remote) != 2 || got.Remote[0].Body != "Spoiler: the butler did it" || !got.Remote[0].Spoiler || got.Remote[0].User != "alice@"+hostOf(a) {
		t.Fatalf("bob should see alice's spoiler-flagged comment from the other server: %+v", got)
	}
	if got.Remote[1].Body != "and a reply" || got.Remote[1].ParentID != got.Remote[0].ID || got.Remote[0].ID >= 0 {
		t.Fatalf("the reply nests under its parent and ids are negative: %+v", got)
	}

	var rcID int64 = -got.Remote[0].ID
	if _, err := b.store.DB.Exec(`UPDATE remote_comments SET expires_at = ?`, time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
	alice.post("/api/comments/"+itoa64(top)+"/edit", map[string]any{"body": "Spoiler: edited text", "spoiler": true})
	a.flush(t)
	if _, err := b.store.DB.Exec(`UPDATE remote_comments SET body = 'stale', expires_at = ?`, time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
	b.fed.RenewLeases(ctx)
	if got := remoteOf(t, bob, epB.ID); got.Remote[0].Body != "Spoiler: edited text" {
		t.Fatalf("renewal brings the edit across: %+v", got)
	}
	var exp string
	b.store.DB.QueryRow(`SELECT expires_at FROM remote_comments WHERE id = ?`, rcID).Scan(&exp)
	if exp <= time.Now().Add(20*time.Hour).UTC().Format("2006-01-02 15:04:05") {
		t.Fatalf("renewal extends the lease to a day: %s", exp)
	}

	if code, out := bob.post("/api/remote-comments/"+itoa64(rcID)+"/report", map[string]any{"reason": "rude"}); code != 200 {
		t.Fatalf("report: %d %v", code, out)
	}
	b.flush(t)
	reports, err := a.store.OpenAbuseReports(10)
	if err != nil || len(reports) != 1 || reports[0].Body != "Spoiler: edited text" {
		t.Fatalf("the report reaches the home server's queue: %v %+v", err, reports)
	}

	a.store.HideUserComments(a.mustUser(t, "alice").ID)
	b.store.DB.Exec(`UPDATE remote_comments SET expires_at = ?`, time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05"))
	b.fed.RenewLeases(ctx)
	if got := remoteOf(t, bob, epB.ID); len(got.Remote) != 0 {
		t.Fatalf("a comment that is hidden at home answers gone, and the copy and its reply are deleted: %+v", got)
	}

}

func TestReplicatedCommentLifecycle(t *testing.T) {
	a, b := newEnv(t), newEnv(t)
	ctx := context.Background()
	alice := a.signup(t, "alice", true)
	bob := b.signup(t, "bobby", true)
	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	bob.post("/api/show/555/follow", map[string]string{"status": "watching"})
	a.fed.Connect(ctx, hostOf(b))
	b.fed.Connect(ctx, hostOf(a))
	a.store.SetPeerShare(hostOf(b), true)
	b.store.SetPeerShare(hostOf(a), true)
	shA, _ := a.store.ShowByTMDB(555)
	shB, _ := b.store.ShowByTMDB(555)
	epA, _ := a.store.EpisodeByNumber(shA.ID, 1, 1)
	epB, _ := b.store.EpisodeByNumber(shB.ID, 1, 1)
	listA := "/api/episode/" + itoa64(epA.ID) + "/comments"

	alice.post(listA, map[string]any{"body": "first thought"})
	a.flush(t)
	if got := remoteOf(t, bob, epB.ID); len(got.Remote) != 1 {
		t.Fatalf("shared: %+v", got)
	}
	var cid int64
	a.store.DB.QueryRow(`SELECT id FROM comments WHERE body = 'first thought'`).Scan(&cid)
	alice.post("/api/comments/"+itoa64(cid)+"/delete", nil)
	a.flush(t)
	if got := remoteOf(t, bob, epB.ID); len(got.Remote) != 0 {
		t.Fatalf("deleting at home withdraws the copy at once: %+v", got)
	}

	alice.post(listA, map[string]any{"body": "second thought"})
	a.flush(t)
	if got := remoteOf(t, bob, epB.ID); len(got.Remote) != 1 {
		t.Fatalf("shared again: %+v", got)
	}
	b.store.Block(b.mustUser(t, "bobby").ID, "alice@"+hostOf(a))
	if got := remoteOf(t, bob, epB.ID); len(got.Remote) != 0 {
		t.Fatalf("blocking the author hides the copy: %+v", got)
	}
	b.store.Unblock(b.mustUser(t, "bobby").ID, "alice@"+hostOf(a))

	b.store.SetPeerShare(hostOf(a), false)
	b.store.DB.Exec(`UPDATE remote_comments SET expires_at = ?`, time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05"))
	b.fed.RenewLeases(ctx)
	if got := remoteOf(t, bob, epB.ID); len(got.Remote) != 0 {
		t.Fatalf("taking a server off the list drops its copies: %+v", got)
	}
	alice.post(listA, map[string]any{"body": "after unlisting"})
	a.flush(t)
	var n int
	b.store.DB.QueryRow(`SELECT COUNT(*) FROM remote_comments`).Scan(&n)
	if n != 0 {
		t.Fatalf("a server that is not allow-listed is not accepted from: %d", n)
	}
	_ = strings.TrimSpace
}

func TestActivityGoesToAllowListedServersButOnlyFriendsSeeIt(t *testing.T) {
	a, b := newEnv(t), newEnv(t)
	ctx := context.Background()
	alice := a.signup(t, "alice", true)
	bob := b.signup(t, "bobby", true)
	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	a.fed.Connect(ctx, hostOf(b))
	b.fed.Connect(ctx, hostOf(a))
	a.store.SetPeerShare(hostOf(b), true)
	b.store.SetPeerShare(hostOf(a), true)
	sh, _ := a.store.ShowByTMDB(555)
	ep, _ := a.store.EpisodeByNumber(sh.ID, 1, 1)
	alice.post("/api/episode/"+itoa64(ep.ID)+"/watch", map[string]any{"watched": true})
	a.flush(t)
	var n int
	b.store.DB.QueryRow(`SELECT COUNT(*) FROM remote_events`).Scan(&n)
	if n != 1 {
		t.Fatalf("an allow-listed server stores the activity of people it has no friendship with: %d", n)
	}
	if html, _ := bob.getJSON("/api/feed")["html"].(string); strings.Contains(html, "alice@") {
		t.Fatalf("but only friends see it in their feed")
	}
}

func TestRemoteCommentsAreWovenIntoTheLocalThreadByTime(t *testing.T) {
	a, b := newEnv(t), newEnv(t)
	ctx := context.Background()
	alice := a.signup(t, "alice", true)
	bob := b.signup(t, "bobby", true)
	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	bob.post("/api/show/555/follow", map[string]string{"status": "watching"})
	a.fed.Connect(ctx, hostOf(b))
	b.fed.Connect(ctx, hostOf(a))
	a.store.SetPeerShare(hostOf(b), true)
	b.store.SetPeerShare(hostOf(a), true)
	shA, _ := a.store.ShowByTMDB(555)
	shB, _ := b.store.ShowByTMDB(555)
	epA, _ := a.store.EpisodeByNumber(shA.ID, 1, 1)
	epB, _ := b.store.EpisodeByNumber(shB.ID, 1, 1)
	alice.post("/api/episode/"+itoa64(epA.ID)+"/comments", map[string]any{"body": "from alice"})
	a.flush(t)
	b.store.DB.Exec(`UPDATE remote_comments SET created_at = '2020-01-01 00:00:00'`)
	bob.post("/api/episode/"+itoa64(epB.ID)+"/comments", map[string]any{"body": "from bobby"})
	_, raw := bob.get("/api/episode/" + itoa64(epB.ID) + "/comments")
	bobAt, aliceAt := strings.Index(raw, "from bobby"), strings.Index(raw, "from alice")
	if bobAt < 0 || aliceAt < 0 || bobAt > aliceAt {
		t.Fatalf("one list, newest first, no separate section: %s", raw)
	}
	if strings.Contains(raw, `"remote"`) {
		t.Fatalf("there is no separate remote section any more")
	}
}

func TestRepliesAcrossServers(t *testing.T) {
	a, b := newEnv(t), newEnv(t)
	ctx := context.Background()
	alice := a.signup(t, "alice", true)
	bob := b.signup(t, "bobby", true)
	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	bob.post("/api/show/555/follow", map[string]string{"status": "watching"})
	a.fed.Connect(ctx, hostOf(b))
	b.fed.Connect(ctx, hostOf(a))
	a.store.SetPeerShare(hostOf(b), true)
	b.store.SetPeerShare(hostOf(a), true)
	shA, _ := a.store.ShowByTMDB(555)
	shB, _ := b.store.ShowByTMDB(555)
	epA, _ := a.store.EpisodeByNumber(shA.ID, 1, 1)
	epB, _ := b.store.EpisodeByNumber(shB.ID, 1, 1)
	listA := "/api/episode/" + itoa64(epA.ID) + "/comments"
	listB := "/api/episode/" + itoa64(epB.ID) + "/comments"

	alice.post(listA, map[string]any{"body": "alice asks something"})
	a.flush(t)
	got := remoteOf(t, bob, epB.ID)
	if len(got.Remote) != 1 {
		t.Fatalf("bob sees alice's comment: %+v", got)
	}
	if code, out := bob.post(listB, map[string]any{"body": "bobby answers across servers", "parent_id": got.Remote[0].ID}); code != 200 {
		t.Fatalf("reply to a comment on another server: %d %v", code, out)
	}
	_, rawB := bob.get(listB)
	if strings.Index(rawB, "alice asks something") > strings.Index(rawB, "bobby answers across servers") || !strings.Contains(rawB, "bobby answers") {
		t.Fatalf("on bob's server the reply sits under the comment it answers: %s", rawB)
	}
	var topsB int
	b.store.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE parent_id IS NULL AND parent_ref = ''`).Scan(&topsB)
	if topsB != 0 {
		t.Fatalf("it is a reply, not a new top-level comment")
	}

	b.flush(t)
	_, rawA := alice.get(listA)
	if !strings.Contains(rawA, "bobby answers across servers") || !strings.Contains(rawA, `"user":"bobby@`+hostOf(b)+`"`) {
		t.Fatalf("on alice's server bob's reply shows under her comment: %s", rawA)
	}
	var top struct {
		Comments []struct {
			ID       int64  `json:"id"`
			Body     string `json:"body"`
			ParentID int64  `json:"parent_id"`
		}
	}
	json.Unmarshal([]byte(rawA), &top)
	var aliceID int64
	a.store.DB.QueryRow(`SELECT id FROM comments WHERE body = 'alice asks something'`).Scan(&aliceID)
	if len(top.Comments) != 2 || top.Comments[0].ID != aliceID || top.Comments[1].ParentID != aliceID {
		t.Fatalf("the reply is nested under her comment: %+v", top.Comments)
	}
	notes, _ := a.store.Notifications(a.mustUser(t, "alice").ID, 10, 0)
	found := false
	for _, n := range notes {
		if n.Event == "comment_reply" {
			found = true
		}
	}
	if !found {
		t.Fatalf("alice is told about the reply from another server: %+v", notes)
	}

	var replyID int64
	b.store.DB.QueryRow(`SELECT id FROM comments WHERE body = 'bobby answers across servers'`).Scan(&replyID)
	bob.post("/api/comments/"+itoa64(replyID)+"/delete", nil)
	b.flush(t)
	if _, raw := alice.get(listA); strings.Contains(raw, "bobby answers across servers") {
		t.Fatalf("deleting the reply withdraws it from the other server")
	}
}
