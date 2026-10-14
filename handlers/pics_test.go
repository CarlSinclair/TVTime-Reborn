package handlers_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func gifBytes(frames int) []byte {
	pal := color.Palette{color.Black, color.White}
	g := &gif.GIF{}
	for i := 0; i < frames; i++ {
		img := image.NewPaletted(image.Rect(0, 0, 32, 32), pal)
		img.SetColorIndex(i, i, 1)
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	gif.EncodeAll(&buf, g)
	return buf.Bytes()
}

func picOf(t *testing.T, body string) (name, url string) {
	t.Helper()
	var out struct{ Name, URL string }
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.Name == "" {
		t.Fatalf("upload answer: %q (%v)", body, err)
	}
	return out.Name, out.URL
}

func TestCommentsCarryPicturesAndGifs(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bob", true)
	if code, out := alice.post("/api/show/555/follow", map[string]string{"status": "watching"}); code != 200 {
		t.Fatalf("follow: %d %v", code, out)
	}
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	path := "/api/episode/" + itoa64(eps[0].ID) + "/comments"

	if code, _ := alice.post(path, map[string]any{"body": ""}); code != 400 {
		t.Fatalf("an empty comment with no picture is refused: %d", code)
	}
	if code, body := upload(t, alice, "/api/pics", []byte("not an image at all")); code != 400 {
		t.Fatalf("a file that is not an image is refused: %d %s", code, body)
	}
	code, body := upload(t, alice, "/api/pics", pngBytes(300, 200))
	if code != 200 {
		t.Fatalf("upload: %d %s", code, body)
	}
	name, url := picOf(t, body)
	if !strings.HasSuffix(name, ".png") || !strings.HasPrefix(name, itoa64(e.mustUser(t, "alice").ID)+"-") {
		t.Fatalf("name %q", name)
	}
	if code, _ := bob.post(path, map[string]any{"body": "mine now", "image": name}); code != 400 {
		t.Fatalf("someone else's upload cannot be attached: %d", code)
	}
	if code, out := alice.post(path, map[string]any{"body": "", "image": name}); code != 200 {
		t.Fatalf("a picture alone is a comment: %d %v", code, out)
	}
	resp, err := http.Get(e.srv.URL + url)
	if err != nil {
		t.Fatal(err)
	}
	served, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || len(served) == 0 {
		t.Fatalf("the picture is served: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	code, body = upload(t, alice, "/api/pics", gifBytes(3))
	if code != 200 {
		t.Fatalf("gif upload: %d %s", code, body)
	}
	gname, gurl := picOf(t, body)
	if !strings.HasSuffix(gname, ".gif") {
		t.Fatalf("a GIF keeps being a GIF: %q", gname)
	}
	if code, out := alice.post(path, map[string]any{"body": "look", "image": gname}); code != 200 {
		t.Fatalf("gif comment: %d %v", code, out)
	}
	resp, _ = http.Get(e.srv.URL + gurl)
	gotGif, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "image/gif" || !bytes.Equal(gotGif, gifBytes(3)) {
		t.Fatalf("the animation is stored untouched: %s", resp.Header.Get("Content-Type"))
	}

	_, list := bob.get(path)
	if !strings.Contains(list, `"image":"`+url+`"`) || !strings.Contains(list, `"image":"`+gurl+`"`) {
		t.Fatalf("comments list their pictures: %s", list)
	}

	var parsed struct {
		Comments []struct {
			ID    int64
			Image string
		}
	}
	json.Unmarshal([]byte(list), &parsed)
	var picID int64
	for _, c := range parsed.Comments {
		if c.Image == url {
			picID = c.ID
		}
	}
	if code, _ := alice.post("/api/comments/"+itoa64(picID)+"/delete", nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	resp, _ = http.Get(e.srv.URL + url)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("deleting the comment removes its picture file: %d", resp.StatusCode)
	}
}

func TestEpisodeStillsCanBeAttached(t *testing.T) {
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
	if code, out := alice.post("/api/show/555/follow", map[string]string{"status": "watching"}); code != 200 {
		t.Fatalf("follow: %d %v", code, out)
	}
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	id := itoa64(eps[1].ID)

	code, list := alice.get("/api/episode/" + id + "/stills")
	if code != 200 || !strings.Contains(list, "/stillAAAA1111.jpg") || !strings.Contains(list, "/stillBBBB2222.jpg") {
		t.Fatalf("the episode's stills are listed: %d %s", code, list)
	}
	if code, _ := alice.post("/api/pics/still", map[string]any{"episode": eps[1].ID, "file": "/notlisted9999.jpg"}); code != 400 {
		t.Fatalf("only a listed still can be used: %d", code)
	}
	code, out := alice.post("/api/pics/still", map[string]any{"episode": eps[1].ID, "file": "/stillAAAA1111.jpg"})
	if code != 200 || out["name"] == "" {
		t.Fatalf("use a still: %d %v", code, out)
	}
	name, _ := out["name"].(string)
	if code, o := alice.post("/api/episode/"+id+"/comments", map[string]any{"body": "this scene", "image": name}); code != 200 {
		t.Fatalf("comment with a still: %d %v", code, o)
	}
}

func TestMarkingAnEpisodeAsksAboutEarlierOnes(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	if code, out := alice.post("/api/show/555/follow", map[string]string{"status": "watching"}); code != 200 {
		t.Fatalf("follow: %d %v", code, out)
	}
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	second := "/api/episode/" + itoa64(eps[1].ID) + "/watch"

	_, page := alice.get("/episode/" + itoa64(eps[1].ID))
	if strings.Contains(page, "data-watch-upto") {
		t.Fatalf("there is no separate up-to button any more")
	}
	code, out := alice.post(second, map[string]any{"watched": true})
	if code != 200 || out["needs_confirm"] != true || out["missing"] != float64(1) {
		t.Fatalf("marking episode 2 first asks about episode 1: %d %v", code, out)
	}
	if ep, _ := e.store.Episode(eps[1].ID, e.mustUser(t, "alice").ID); ep.Watched {
		t.Fatalf("nothing is marked until the person answers")
	}
	if code, out := alice.post(second, map[string]any{"watched": true, "single": true}); code != 200 || out["needs_confirm"] != nil {
		t.Fatalf("answering no marks just this one: %d %v", code, out)
	}
	if ep, _ := e.store.Episode(eps[0].ID, e.mustUser(t, "alice").ID); ep.Watched {
		t.Fatalf("the earlier episode stays unwatched on no")
	}
	if code, _ := alice.post(second, map[string]any{"watched": false}); code != 200 {
		t.Fatalf("unwatch: %d", code)
	}
	if code, out := alice.post(second, map[string]any{"upTo": true}); code != 200 || out["marked"] != float64(2) {
		t.Fatalf("answering yes marks everything up to here: %d %v", code, out)
	}
	if ep, _ := e.store.Episode(eps[0].ID, e.mustUser(t, "alice").ID); !ep.Watched {
		t.Fatalf("the earlier episode is marked on yes")
	}
	first := "/api/episode/" + itoa64(eps[0].ID) + "/watch"
	alice.post(first, map[string]any{"watched": false})
	if code, out := alice.post(first, map[string]any{"watched": true}); code != 200 || out["needs_confirm"] != nil {
		t.Fatalf("the first episode never asks: %d %v", code, out)
	}
}

func TestAbandonedUploadsAreSweptAndDiscardedOnesGoAtOnce(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bob", true)
	upl := func() (string, string) {
		code, body := upload(t, alice, "/api/pics", pngBytes(64, 64))
		if code != 200 {
			t.Fatalf("upload: %d %s", code, body)
		}
		return picOf(t, body)
	}
	gone := func(url string) bool {
		resp, err := http.Get(e.srv.URL + url)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode == 404
	}

	name, url := upl()
	if code, _ := bob.post("/api/pics/discard", map[string]any{"name": name}); code != 400 {
		t.Fatalf("someone else cannot discard it: %d", code)
	}
	if code, _ := alice.post("/api/pics/discard", map[string]any{"name": name}); code != 200 || !gone(url) {
		t.Fatalf("taking the picture off the draft deletes it at once")
	}

	name, url = upl()
	e.app.SweepPics(-time.Minute)
	if !gone(url) {
		t.Fatalf("an upload nobody used is swept")
	}
	_ = name

	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	name, url = upl()
	if code, out := alice.post("/api/episode/"+itoa64(eps[0].ID)+"/comments", map[string]any{"body": "x", "image": name}); code != 200 {
		t.Fatalf("comment: %d %v", code, out)
	}
	e.app.SweepPics(-time.Minute)
	if gone(url) {
		t.Fatalf("a picture a comment shows is never swept")
	}
	if code, _ := alice.post("/api/pics/discard", map[string]any{"name": name}); code != 200 || gone(url) {
		t.Fatalf("discard leaves a used picture alone")
	}
}

func TestLargeAnimatedGifsAreAccepted(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	big := gifBytes(120)
	pad := make([]byte, 7<<20)
	withPad := append(append([]byte{}, big[:len(big)-1]...), pad...)
	withPad = append(withPad, 0x3B)
	if code, body := upload(t, alice, "/api/pics", withPad); code != 400 {
		t.Fatalf("garbage inside the GIF structure is refused: %d %s", code, body)
	}
	if code, body := upload(t, alice, "/api/pics", big); code != 200 {
		t.Fatalf("a many-frame GIF is accepted: %d %s", code, body)
	}
}

func TestEditorSourcesAreServedFromThisOrigin(t *testing.T) {
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
	bob := e.signup(t, "bob", true)
	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	id := itoa64(eps[1].ID)

	code, body := upload(t, alice, "/api/pics", pngBytes(90, 60))
	if code != 200 {
		t.Fatalf("upload: %d %s", code, body)
	}
	name, _ := picOf(t, body)
	if code, got := alice.get("/api/pics/source/" + name); code != 200 || len(got) == 0 {
		t.Fatalf("the owner can load their picture into the editor: %d", code)
	}
	if code, _ := bob.get("/api/pics/source/" + name); code != 404 {
		t.Fatalf("nobody else can: %d", code)
	}

	if code, got := alice.get("/api/episode/" + id + "/still?file=%2FstillAAAA1111.jpg"); code != 200 || len(got) == 0 {
		t.Fatalf("a listed still comes back at full size: %d", code)
	}
	if code, _ := alice.get("/api/episode/" + id + "/still?file=%2Fnotlisted9999.jpg"); code != 400 {
		t.Fatalf("an unlisted still is refused: %d", code)
	}
	if list, _ := e.store.PicUploadsBefore("9999-01-01 00:00:00"); len(list) != 1 {
		t.Fatalf("loading stills into the editor stores nothing: %v", list)
	}
}
