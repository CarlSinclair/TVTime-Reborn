package handlers_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func hostOf(e *env) string { return strings.TrimPrefix(e.srv.URL, "http://") }

func (e *env) flush(t *testing.T) { e.fed.Flush(context.Background()) }

func TestFriendsAcrossServers(t *testing.T) {
	a, b := newEnv(t), newEnv(t)
	alice := a.signup(t, "alice", true)
	bob := b.signup(t, "bobby", true)
	bobHandle := "bobby@" + hostOf(b)
	aliceHandle := "alice@" + hostOf(a)

	out := alice.getJSON("/api/people?q=" + bobHandle)
	res, _ := out["results"].([]any)
	if len(res) != 1 {
		t.Fatalf("a person on another server must be found by name@host: %v", out)
	}
	if out := alice.getJSON("/api/people?q=nobody@" + hostOf(b)); len(out["results"].([]any)) != 0 {
		t.Fatalf("an unknown name on a real server finds nobody: %v", out)
	}
	if out := alice.getJSON("/api/people?q=bobby@127.0.0.1:1"); out["remote"] != true {
		t.Fatalf("an unreachable server must be reported: %v", out)
	}

	if code, out := alice.post("/api/friends/request", map[string]string{"username": bobHandle}); code != 200 || out["state"] != "outgoing" {
		t.Fatalf("cross-server request: %d %v", code, out)
	}
	a.flush(t)
	in := bob.getJSON("/api/friends")["incoming"].([]any)
	if len(in) != 1 || in[0].(map[string]any)["username"] != aliceHandle {
		t.Fatalf("bob should see a request from %s: %v", aliceHandle, in)
	}
	bu := b.mustUser(t, "bobby")
	notes, _ := b.store.Notifications(bu.ID, 5, 0)
	if len(notes) == 0 || notes[0].Event != "friend_request" {
		t.Fatalf("the request must notify bob: %+v", notes)
	}
	_, page := bob.get("/notifications")
	if !strings.Contains(page, `data-friend-accept="`+aliceHandle+`"`) {
		t.Fatalf("the notification needs accept and decline for the remote person")
	}

	if code, out := bob.post("/api/friends/respond", map[string]any{"username": aliceHandle, "accept": true}); code != 200 || out["state"] != "friends" {
		t.Fatalf("accept: %d %v", code, out)
	}
	b.flush(t)
	fl := alice.getJSON("/api/friends")["friends"].([]any)
	if len(fl) != 1 || fl[0].(map[string]any)["username"] != bobHandle {
		t.Fatalf("alice should now list bob as a friend: %v", fl)
	}
	_, prof := alice.get("/u/" + bobHandle)
	if !strings.Contains(prof, `data-friend-remove="`+bobHandle+`"`) || !strings.Contains(prof, "View on their server") {
		t.Fatalf("the remote profile shows the friend controls and a link out")
	}

	bob.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh, _ := b.store.ShowByTMDB(555)
	ep1, _ := b.store.EpisodeByNumber(sh.ID, 1, 1)
	bob.post("/api/episode/"+itoa64(ep1.ID)+"/watch", map[string]any{"watched": true})
	bob.post("/api/episode/"+itoa64(ep1.ID)+"/rate", map[string]any{"rating": 10})
	bob.post("/api/episode/"+itoa64(ep1.ID)+"/comments", map[string]any{"body": "Great remote pilot"})
	bob.post("/api/episode/"+itoa64(ep1.ID)+"/comments", map[string]any{"body": "Secret remote twist", "spoiler": true})
	b.flush(t)

	html, _ := alice.getJSON("/api/feed")["html"].(string)
	for _, want := range []string{bobHandle, "watched an episode of", "rated an episode of", "Great remote pilot", "Fake Show"} {
		if !strings.Contains(html, want) {
			t.Errorf("remote activity must reach the feed; missing %q in %q", want, html)
		}
	}
	if strings.Contains(html, "Secret remote twist") {
		t.Fatalf("spoilers never leave their server")
	}

	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	ash, _ := a.store.ShowByTMDB(555)
	aep1, _ := a.store.EpisodeByNumber(ash.ID, 1, 1)
	alice.post("/api/episode/"+itoa64(aep1.ID)+"/watch", map[string]any{"watched": true})
	html, _ = alice.getJSON("/api/feed")["html"].(string)
	if !strings.Contains(html, "watched S01E01 of") {
		t.Fatalf("once alice has seen the episode it is named: %q", html)
	}

	if code, _ := alice.post("/api/friends/remove", map[string]string{"username": bobHandle}); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	a.flush(t)
	if fl := bob.getJSON("/api/friends")["friends"].([]any); len(fl) != 0 {
		t.Fatalf("ending a friendship must reach the other server: %v", fl)
	}
	bob.post("/api/episode/"+itoa64(ep1.ID)+"/comments", map[string]any{"body": "after the breakup"})
	b.flush(t)
	html, _ = alice.getJSON("/api/feed")["html"].(string)
	if strings.Contains(html, "after the breakup") {
		t.Fatalf("no activity flows after a friendship ends")
	}
}

func inboxRequest(t *testing.T, target *env, senderHost string, priv ed25519.PrivateKey, body []byte, mutate func(date, digest *string, sig []byte) []byte) int {
	date := strconv.FormatInt(time.Now().Unix(), 10)
	sum := sha256.Sum256(body)
	digest := base64.StdEncoding.EncodeToString(sum[:])
	canon := []byte("TVT1\nPOST\n/federation/inbox\n" + hostOf(target) + "\n" + date + "\n" + digest)
	sig := ed25519.Sign(priv, canon)
	if mutate != nil {
		sig = mutate(&date, &digest, sig)
	}
	req, _ := http.NewRequest("POST", target.srv.URL+"/federation/inbox", bytes.NewReader(body))
	req.Header.Set("X-TVT-Host", senderHost)
	req.Header.Set("X-TVT-Date", date)
	req.Header.Set("X-TVT-Digest", digest)
	req.Header.Set("X-TVT-Signature", base64.StdEncoding.EncodeToString(sig))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestInboxRejectsForgeries(t *testing.T) {
	a, b := newEnv(t), newEnv(t)
	b.signup(t, "bobby", true)
	body := []byte(`{"type":"friend_request","id":"x","from":{"username":"mallory","name":"M"},"to":"bobby"}`)

	imposterPub, imposterPriv, _ := ed25519.GenerateKey(nil)
	_ = imposterPub
	if code := inboxRequest(t, b, hostOf(a), imposterPriv, body, nil); code != 401 {
		t.Fatalf("a message signed with the wrong key must be refused: %d", code)
	}
	if code := inboxRequest(t, b, hostOf(a), imposterPriv, body, func(d, g *string, sig []byte) []byte { *d = "1"; return sig }); code != 401 {
		t.Fatalf("a stale date must be refused: %d", code)
	}
	if code := inboxRequest(t, b, hostOf(a), imposterPriv, body, func(d, g *string, sig []byte) []byte { *g = "AAAA"; return sig }); code != 401 {
		t.Fatalf("a body that does not match its digest must be refused: %d", code)
	}
	req, _ := http.NewRequest("POST", b.srv.URL+"/federation/inbox", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 204 || resp.StatusCode == 200 {
		t.Fatalf("an unsigned message must never be accepted: %d", resp.StatusCode)
	}
	bu := b.mustUser(t, "bobby")
	if in, _ := b.store.IncomingFriendRequests(bu.ID); len(in) != 0 {
		t.Fatalf("no forged request may land: %v", in)
	}
}
