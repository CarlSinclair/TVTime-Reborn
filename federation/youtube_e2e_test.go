package federation

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"tvt/db"
	"tvt/store"
)

const (
	e2eSender = "send.example.test"
	e2eSource = "UCaaaaaaaaaaaaaaaaaaaaaa"
)

type e2eServer struct {
	st  *store.Store
	svc *Service
}

func newE2EServer(t *testing.T, host string) *e2eServer {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), host+".db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(d)
	svc, err := New(st, nil, host, "0123456789abcdef0123456789abcdef", true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &e2eServer{st: st, svc: svc}
}

func e2eVideo(id, title, when string) store.YouTubeVideo {
	p, err := time.Parse(time.RFC3339, when)
	if err != nil {
		panic(err)
	}
	return store.YouTubeVideo{ID: id, Title: title, Published: p}
}

func (a *e2eServer) show(t *testing.T, id int64, vids ...store.YouTubeVideo) {
	t.Helper()
	showID, err := a.st.SaveYouTubeShow(&store.Show{TMDBID: id, Name: "Chan", Networks: "YouTube"}, e2eSource)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := a.st.ApplyYouTubeVideos(showID, vids, true); err != nil || n != len(vids) {
		t.Fatalf("apply videos: added=%d err=%v", n, err)
	}
}

// deliver hands every queued message of the sender to the receiver's inbox the
// way Flush would over the network, signed with the sender's key.
func deliver(t *testing.T, from, to *e2eServer, toHost string) int {
	t.Helper()
	items, err := from.st.DueOutbox(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Host != toHost {
			t.Fatalf("queued for %s, want %s", it.Host, toHost)
		}
		body := []byte(it.Body)
		date := strconv.FormatInt(time.Now().Unix(), 10)
		digest := digestOf(body)
		sig := ed25519.Sign(from.svc.priv, canonical("POST", it.Path, toHost, date, digest))
		req := httptest.NewRequest("POST", it.Path, bytes.NewReader(body))
		req.RemoteAddr = "203.0.113.9:4000"
		req.Header.Set("X-TVT-Host", from.svc.Host)
		req.Header.Set("X-TVT-Date", date)
		req.Header.Set("X-TVT-Digest", digest)
		req.Header.Set("X-TVT-Signature", base64.StdEncoding.EncodeToString(sig))
		rec := httptest.NewRecorder()
		to.svc.Inbox(rec, req)
		if rec.Code < 200 || rec.Code >= 300 {
			t.Fatalf("inbox status %d: %s", rec.Code, rec.Body.String())
		}
		if err := from.st.OutboxDone(it.ID); err != nil {
			t.Fatal(err)
		}
	}
	return len(items)
}

func TestYouTubeFederationEndToEnd(t *testing.T) {
	snd := newE2EServer(t, e2eSender)
	rcv := newE2EServer(t, recvHost)
	if err := rcv.st.PinPeer(e2eSender, base64.StdEncoding.EncodeToString(snd.svc.pub)); err != nil {
		t.Fatal(err)
	}
	if err := snd.st.PinPeer(recvHost, base64.StdEncoding.EncodeToString(rcv.svc.pub)); err != nil {
		t.Fatal(err)
	}
	if err := snd.st.SetPeerShare(recvHost, true); err != nil {
		t.Fatal(err)
	}
	if err := rcv.st.SetPeerShare(e2eSender, true); err != nil {
		t.Fatal(err)
	}

	snd.show(t, -111,
		e2eVideo("AAAAAAAAAAA", "First", "2026-11-09T10:00:00Z"),
		e2eVideo("BBBBBBBBBBB", "Second", "2026-11-10T10:00:00Z"),
		e2eVideo("CCCCCCCCCCC", "Third", "2026-11-11T10:00:00Z"),
	)
	rcv.show(t, -999,
		e2eVideo("BBBBBBBBBBB", "Second", "2026-11-10T10:00:00Z"),
		e2eVideo("CCCCCCCCCCC", "Third", "2026-11-11T10:00:00Z"),
	)

	alice, err := snd.st.CreateUser("alice@x.io", "alice", "", "en", false)
	if err != nil {
		t.Fatal(err)
	}
	alice.ShareActivity = true

	sSeason, sEp, ok, err := snd.st.YouTubeEpisode(-111, "BBBBBBBBBBB")
	if err != nil || !ok || sSeason != 2026 || sEp != 2 {
		t.Fatalf("sender numbering s%d e%d ok=%v err=%v", sSeason, sEp, ok, err)
	}
	rSeason, rEp, ok, err := rcv.st.YouTubeEpisode(-999, "BBBBBBBBBBB")
	if err != nil || !ok || rSeason != 2026 || rEp != 1 {
		t.Fatalf("receiver numbering s%d e%d ok=%v err=%v", rSeason, rEp, ok, err)
	}

	snd.svc.Emit(alice, Event{Ref: "w:known", Kind: "watch", ShowTMDB: -111, Show: "Chan", Season: sSeason, Episode: sEp})
	snd.svc.Emit(alice, Event{Ref: "w:unknown", Kind: "watch", ShowTMDB: -111, Show: "Chan", Season: 2026, Episode: 1})
	if n := deliver(t, snd, rcv, recvHost); n != 2 {
		t.Fatalf("delivered %d messages, want 2", n)
	}

	rows, err := rcv.st.DB.Query(`SELECT ref, show_tmdb, season, episode FROM remote_events ORDER BY ref`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type got struct {
		ref             string
		show            int64
		season, episode int
	}
	var all []got
	for rows.Next() {
		var g got
		if err := rows.Scan(&g.ref, &g.show, &g.season, &g.episode); err != nil {
			t.Fatal(err)
		}
		all = append(all, g)
	}
	if len(all) != 1 {
		t.Fatalf("receiver stored %d events, want only the one it can place: %+v", len(all), all)
	}
	if all[0].ref != "w:known" || all[0].show != -999 || all[0].season != 2026 || all[0].episode != 1 {
		t.Fatalf("event landed on %+v, want the receiver's own show -999 s2026 e1", all[0])
	}

	snd.svc.ShareComment(alice, CommentMsg{Ref: "c:1", ShowTMDB: -111, YT: e2eSource, YTV: "CCCCCCCCCCC", Season: 2026, Episode: 3, Body: "great one", Created: store.Now()})
	snd.svc.ShareComment(alice, CommentMsg{Ref: "c:2", ShowTMDB: -111, YT: e2eSource, YTV: "AAAAAAAAAAA", Season: 2026, Episode: 1, Body: "not here yet", Created: store.Now()})
	if n := deliver(t, snd, rcv, recvHost); n != 2 {
		t.Fatalf("delivered %d comments, want 2", n)
	}
	viewer, err := rcv.st.CreateUser("bob@x.io", "bob", "", "en", false)
	if err != nil {
		t.Fatal(err)
	}
	onThird, err := rcv.st.RemoteCommentsFor(-999, 2026, 2, viewer.ID)
	if err != nil || len(onThird) != 1 || onThird[0].Ref != "c:1" {
		t.Fatalf("comment on the receiver's e2: %+v err=%v", onThird, err)
	}
	if wrong, err := rcv.st.RemoteCommentsFor(-999, 2026, 3, viewer.ID); err != nil || len(wrong) != 0 {
		t.Fatalf("comment must not sit on the sender's number: %+v err=%v", wrong, err)
	}
	if lost, err := rcv.st.RemoteCommentsFor(-111, 2026, 1, viewer.ID); err != nil || len(lost) != 0 {
		t.Fatalf("a comment on an unknown video must be dropped: %+v err=%v", lost, err)
	}

	snd.svc.ShareRatings(alice, []store.Rating{
		{Kind: "episode", ShowTMDB: -111, Season: 2026, Episode: 3, Rating: 9},
		{Kind: "episode", ShowTMDB: -111, Season: 2026, Episode: 1, Rating: 4},
		{Kind: "show", ShowTMDB: -111, Rating: 8},
		{Kind: "show", ShowTMDB: -5555, Rating: 7},
		{Kind: "show", ShowTMDB: -(1 << 41) - 77, Rating: 6},
	})
	if n := deliver(t, snd, rcv, recvHost); n != 1 {
		t.Fatalf("delivered %d rating messages, want 1", n)
	}
	var rated int
	if err := rcv.st.DB.QueryRow(`SELECT COUNT(*) FROM remote_ratings`).Scan(&rated); err != nil || rated != 3 {
		t.Fatalf("receiver kept %d ratings, want the two it can place plus the TVDB show that keeps its id: %v", rated, err)
	}
	var stars int
	if err := rcv.st.DB.QueryRow(`SELECT rating FROM remote_ratings WHERE kind = 'episode' AND show_tmdb = -999 AND season = 2026 AND episode = 2`).Scan(&stars); err != nil || stars != 9 {
		t.Fatalf("episode rating must land on the receiver's own numbering: %d %v", stars, err)
	}
	if err := rcv.st.DB.QueryRow(`SELECT rating FROM remote_ratings WHERE kind = 'show' AND show_tmdb = -999`).Scan(&stars); err != nil || stars != 8 {
		t.Fatalf("show rating must land on the receiver's own show: %d %v", stars, err)
	}

	var shows int
	if err := rcv.st.DB.QueryRow(`SELECT COUNT(*) FROM shows WHERE yt_source = ?`, e2eSource).Scan(&shows); err != nil || shows != 1 {
		t.Fatalf("receiver must keep one show per source: %d %v", shows, err)
	}
}
