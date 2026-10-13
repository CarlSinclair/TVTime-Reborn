package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tvt/appconfig"
	"tvt/assets"
	"tvt/auth"
	"tvt/config"
	"tvt/db"
	"tvt/federation"
	"tvt/handlers"
	"tvt/i18n"
	"tvt/mailer"
	"tvt/media"
	"tvt/notify"
	"tvt/store"
	"tvt/syncer"
	"tvt/telegram"
	"tvt/tmdb"
	"tvt/tvdb"
)

func fakeTVDB(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"token": "tok"}})
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		if strings.Contains(strings.ToLower(r.URL.Query().Get("query")), "tube") {
			out = append(out, map[string]any{"tvdb_id": "9001", "name": "Tube Channel", "first_air_time": "2010-01-01", "image_url": "https://artworks.thetvdb.com/banners/posters/abc.jpg", "type": "series"})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": out})
	})
	mux.HandleFunc("/series/9001/extended", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"id": 9001, "name": "Tube Channel", "overview": "A channel.", "image": "https://artworks.thetvdb.com/banners/posters/abc.jpg",
			"firstAired": "2010-01-01", "lastAired": "2020-02-01", "status": map[string]any{"name": "Continuing"},
			"originalNetwork": map[string]any{"name": "YouTube"}, "genres": []map[string]any{{"name": "Web"}}, "averageRuntime": 10,
			"remoteIds": []map[string]any{{"id": "tt9001", "sourceName": "IMDB"}},
		}})
	})
	mux.HandleFunc("/series/9001/episodes/default", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"episodes": []map[string]any{
			{"id": 71, "name": "First video", "aired": "2020-01-01", "number": 1, "seasonNumber": 2020, "runtime": 8},
			{"id": 72, "name": "Second video", "aired": "2020-02-01", "number": 2, "seasonNumber": 2020, "runtime": 9},
		}}, "links": map[string]any{"next": nil}})
	})
	mux.HandleFunc("/banners/posters/abc.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Write(pngBytes(1, 1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func fakeTMDB(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	season := func(n int) map[string]any {
		return map[string]any{"season_number": n, "name": fmt.Sprintf("Season %d", n), "episodes": []map[string]any{
			{"id": n*10 + 1, "season_number": n, "episode_number": 1, "name": "Pilot", "air_date": "2020-01-01", "runtime": 40},
			{"id": n*10 + 2, "season_number": n, "episode_number": 2, "name": "Second", "air_date": "2020-01-08", "runtime": 40},
		}}
	}
	mux.HandleFunc("/tv/555", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{
			"id": 555, "name": "Fake Show", "overview": "A show.", "status": "Returning Series", "first_air_date": "2020-01-01",
			"genres": []map[string]string{{"name": "Drama"}}, "networks": []map[string]string{{"name": "Net"}},
			"number_of_seasons": 1, "episode_run_time": []int{40},
			"seasons":      []map[string]any{{"season_number": 1, "name": "Season 1", "episode_count": 2}},
			"external_ids": map[string]any{"imdb_id": "tt555", "tvdb_id": 5550},
		}
		for _, p := range strings.Split(r.URL.Query().Get("append_to_response"), ",") {
			if strings.HasPrefix(p, "season/") {
				out[p] = season(1)
			}
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/find/", func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/find/") == "70001" && r.URL.Query().Get("external_source") == "tvdb_id" {
			json.NewEncoder(w).Encode(map[string]any{"tv_results": []any{}, "tv_episode_results": []map[string]any{{"id": 12, "show_id": 555, "season_number": 1, "episode_number": 2}}})
			return
		}
		if strings.TrimPrefix(r.URL.Path, "/find/") == "5550" && r.URL.Query().Get("external_source") == "tvdb_id" {
			json.NewEncoder(w).Encode(map[string]any{"tv_results": []map[string]any{{"id": 555, "name": "Fake Show"}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"tv_results": []any{}})
	})
	mux.HandleFunc("/search/tv", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": 555, "name": "Fake Show"}}})
	})
	mux.HandleFunc("/trending/tv/week", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": 555, "name": "Fake Show"}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func (c *client) csrf() string {
	u, _ := url.Parse(c.base)
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == auth.CookieSession {
			return auth.BagField(ck.Value, "c")
		}
	}
	return ""
}

func (c *client) get(path string) (int, string) {
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (c *client) post(path string, body any) (int, map[string]any) {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", c.base+path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrf())
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &out); err != nil {
		c.t.Fatalf("POST %s: bad json %q", path, raw)
	}
	return resp.StatusCode, out
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: base, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	c.get("/login")
	return c
}

type env struct {
	srv   *httptest.Server
	store *store.Store
	auth  *auth.Service
	cfg   *config.Config
	tg    *tgFake
	bot   *telegram.Bot
	nt    *notify.Service
	sync  *syncer.Syncer
	app   *handlers.App
	fed   *federation.Service
}

type tgFake struct {
	mu   sync.Mutex
	sent []map[string]any
}

func (f *tgFake) messages() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.sent...)
}

func newTelegramFake(t *testing.T) (*tgFake, string) {
	f := &tgFake{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			if chat, _ := body["chat_id"].(string); chat == "666" {
				w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`))
				return
			}
			f.mu.Lock()
			f.sent = append(f.sent, body)
			n := len(f.sent)
			f.mu.Unlock()
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, 1000+n)))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(d)
	srv := httptest.NewUnstartedServer(nil)
	cfg := &config.Config{AppName: "TVT", BaseURL: "http://" + srv.Listener.Addr().String(), FedInsecure: true, DefaultLang: "en", DevLogMail: true, JWTSecret: "0123456789abcdef0123456789abcdef", DataDir: t.TempDir(), ImageBase: "http://127.0.0.1:1"}
	tm := tmdb.New("testkey")
	tm.Base = fakeTMDB(t).URL
	a := auth.New(st, cfg.JWTSecret, false)
	bundle, err := i18n.Load(assets.FS, "locales", "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	fake, tgURL := newTelegramFake(t)
	bot := telegram.New("123456:test-token", "tvt_test_bot", "whsecret")
	bot.API = tgURL
	ml := mailer.New(cfg)
	nt := notify.New(st, ml, bot, bundle, notify.NewPusher(st, cfg.JWTSecret, "mailto:test@example.com"), cfg.AppName, cfg.BaseURL)
	nt.Inline = true
	if err := a.EnableWebAuthn("localhost", "http://localhost", "TVT"); err != nil {
		t.Fatalf("webauthn: %v", err)
	}
	sy := syncer.New(st, tm)
	tvdbSrv := fakeTVDB(t)
	sy.TVDB = tvdb.New("tvdbkey")
	sy.TVDB.Base = tvdbSrv.URL
	cfg.TVDBImageBase = tvdbSrv.URL
	app, err := handlers.New(cfg, st, a, sy, tm, ml, nt, bot, &media.Disk{Dir: filepath.Join(cfg.DataDir, "media")})
	if err != nil {
		t.Fatalf("app: %v", err)
	}
	app.Settings = appconfig.New(st, cfg.JWTSecret, cfg)
	fed, err := federation.New(st, nt, srv.Listener.Addr().String(), cfg.JWTSecret, true, nil, nil)
	if err != nil {
		t.Fatalf("federation: %v", err)
	}
	app.Fed = fed
	srv.Config.Handler = app.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	return &env{srv: srv, store: st, auth: a, cfg: cfg, tg: fake, bot: bot, nt: nt, sync: sy, app: app, fed: fed}
}

func (e *env) signup(t *testing.T, name string, verify bool) *client {
	c := newClient(t, e.srv.URL)
	code, out := c.post("/api/auth/register", map[string]string{"email": name + "@example.com", "username": name, "password": "Passw0rd!x1"})
	if code != 200 || out["ok"] != true {
		t.Fatalf("register %s: %d %v", name, code, out)
	}
	if verify {
		u, err := e.store.UserByUsername(name)
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if err := e.store.SetEmailVerified(u.ID); err != nil {
			t.Fatalf("verify: %v", err)
		}
	}
	return c
}

func TestPagesRenderForVisitor(t *testing.T) {
	e := newEnv(t)
	anon := newClient(t, e.srv.URL)
	for _, p := range []string{"/", "/welcome", "/login", "/register", "/forgot", "/privacy", "/about", "/offline", "/t/verify?token=x"} {
		code, body := anon.get(p)
		if code != 200 || !strings.Contains(body, "</html>") {
			t.Errorf("anonymous GET %s: status %d", p, code)
		}
	}
	for _, p := range []string{"/shows", "/calendar", "/settings", "/search?q=fake", "/show/555", "/u/someone", "/nope"} {
		resp, err := anon.http.Get(anon.base + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
			t.Errorf("anonymous GET %s: status %d location %q, want redirect to /login", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if code, out := anon.get("/api/search?q=fake"); code != 401 {
		t.Errorf("anonymous API search: %d %v", code, out)
	}
	c := e.signup(t, "viewer", true)
	for _, p := range []string{"/", "/shows", "/calendar", "/search?q=fake", "/show/555", "/show/555/season/1", "/settings", "/about"} {
		code, body := c.get(p)
		if code != 200 || !strings.Contains(body, "</html>") {
			t.Errorf("GET %s: status %d", p, code)
		}
	}
	if code, _ := c.get("/show/999999"); code != 502 && code != 404 {
		t.Errorf("unknown show status %d", code)
	}
	if code, _ := c.get("/nope"); code != 404 {
		t.Errorf("unknown path status %d", code)
	}
}

func TestCSRFRequired(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Post(e.srv.URL+"/api/auth/login", "application/json", strings.NewReader(`{"email":"a@b.co","password":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("POST without CSRF: %d want 403", resp.StatusCode)
	}
}

func TestFullFlow(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bob", true)
	carl := e.signup(t, "carl", false)

	if code, out := alice.post("/api/show/555/follow", map[string]string{"status": "watching"}); code != 200 {
		t.Fatalf("follow: %d %v", code, out)
	}
	if code, _ := alice.get("/"); code != 200 {
		t.Fatalf("home as user: %d", code)
	}
	_, body := alice.get("/")
	if !strings.Contains(body, "Pilot") || !strings.Contains(body, "S01E01") {
		t.Fatalf("up next missing first episode")
	}
	sh, err := e.store.ShowByTMDB(555)
	if err != nil {
		t.Fatalf("show not stored: %v", err)
	}
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	if len(eps) != 2 {
		t.Fatalf("episodes stored: %d", len(eps))
	}

	if code, out := alice.post(fmt.Sprintf("/api/episode/%d/watch", eps[0].ID), map[string]bool{"watched": true}); code != 200 || out["watched"].(float64) != 1 {
		t.Fatalf("watch: %d %v", code, out)
	}

	if code, out := carl.post(fmt.Sprintf("/api/episode/%d/comments", eps[0].ID), map[string]any{"body": "hi"}); code != 403 || out["key"] != "err.verify_email" {
		t.Fatalf("unverified comment: %d %v", code, out)
	}
	code, out := alice.post(fmt.Sprintf("/api/episode/%d/comments", eps[0].ID), map[string]any{"body": "Great pilot", "spoiler": false})
	if code != 200 {
		t.Fatalf("comment: %d %v", code, out)
	}
	cid := int64(out["id"].(float64))
	if code, out := alice.post(fmt.Sprintf("/api/episode/%d/comments", eps[0].ID), map[string]any{"body": "   "}); code != 400 {
		t.Fatalf("blank comment accepted: %d %v", code, out)
	}

	if code, out := bob.post(fmt.Sprintf("/api/comments/%d/report", cid), map[string]string{"kind": "spoiler"}); code != 200 {
		t.Fatalf("report: %d %v", code, out)
	}
	if code, out := bob.post(fmt.Sprintf("/api/comments/%d/report", cid), map[string]string{"kind": "spoiler"}); code != 409 {
		t.Fatalf("duplicate report: %d %v", code, out)
	}
	if code, out := alice.post(fmt.Sprintf("/api/comments/%d/report", cid), map[string]string{"kind": "abuse"}); code != 400 {
		t.Fatalf("self report: %d %v", code, out)
	}

	_, raw := bob.get(fmt.Sprintf("/api/episode/%d/comments", eps[0].ID))
	var list struct {
		Comments []struct {
			ID      int64
			Spoiler bool
			Mine    bool
		}
		ViewerWatched bool `json:"viewer_watched"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list.Comments) != 1 {
		t.Fatalf("list: %v %s", err, raw)
	}
	if !list.Comments[0].Spoiler || list.ViewerWatched {
		t.Fatalf("expected spoiler flag for unwatched bob: %+v", list)
	}

	if code, out := bob.post(fmt.Sprintf("/api/comments/%d/block", cid), nil); code != 200 {
		t.Fatalf("block: %d %v", code, out)
	}
	_, raw = bob.get(fmt.Sprintf("/api/episode/%d/comments", eps[0].ID))
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list.Comments) != 0 {
		t.Fatalf("blocked author still visible: %s", raw)
	}

	if code, _ := alice.get("/admin"); code != 403 {
		t.Fatalf("admin page for non-admin: %d", code)
	}
	if code, out := alice.post("/api/auth/logout", nil); code != 200 {
		t.Fatalf("logout: %d %v", code, out)
	}
	if code, out := alice.post("/api/show/555/unfollow", nil); code != 401 {
		t.Fatalf("unauth action after logout: %d %v", code, out)
	}
}

func TestLoginErrorsAreGeneric(t *testing.T) {
	e := newEnv(t)
	e.signup(t, "dave", true)
	c := newClient(t, e.srv.URL)
	code1, out1 := c.post("/api/auth/login", map[string]string{"email": "dave@example.com", "password": "wrongpass1"})
	code2, out2 := c.post("/api/auth/login", map[string]string{"email": "nobody@example.com", "password": "wrongpass1"})
	if code1 != 401 || code2 != 401 || out1["error"] != out2["error"] {
		t.Fatalf("login failures should be identical: %d %v / %d %v", code1, out1, code2, out2)
	}
	if code, out := c.post("/api/auth/login", map[string]string{"email": "dave@example.com", "password": "Passw0rd!x1"}); code != 200 {
		t.Fatalf("good login: %d %v", code, out)
	}
}
