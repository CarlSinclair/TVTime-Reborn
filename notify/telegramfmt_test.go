package notify

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"tvt/i18n"
	"tvt/store"
	"tvt/telegram"
)

const testLocale = `{"notif":{"episode_aired":{"title":"New {show} episode is out!","body":"{code} {name} & more"},"password_changed":{"title":"Password changed","body":"Your password was changed."}}}`

func testService(t *testing.T) *Service {
	t.Helper()
	b, err := i18n.Load(fstest.MapFS{"locales/en.json": {Data: []byte(testLocale)}}, "locales", "en")
	if err != nil {
		t.Fatalf("load locales: %v", err)
	}
	return &Service{I18n: b, BaseURL: "https://tvt.example"}
}

func TestTelegramHTMLLinksEpisodeCodeAndName(t *testing.T) {
	s := testService(t)
	data := map[string]any{"show": "Show", "code": "S02E05", "name": "The Name"}
	got := s.telegramHTML("en", "episode_aired", data, "https://tvt.example/episode/9")
	want := "<b>New Show episode is out!</b>\n<a href=\"https://tvt.example/episode/9\">S02E05 The Name</a> &amp; more"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if strings.Contains(got, "\nhttps://") || strings.HasSuffix(got, "episode/9") {
		t.Fatalf("bare link present: %q", got)
	}
}

func TestTelegramHTMLLinksTitleForOtherEvents(t *testing.T) {
	s := testService(t)
	got := s.telegramHTML("en", "password_changed", nil, "https://tvt.example/settings")
	want := "<b><a href=\"https://tvt.example/settings\">Password changed</a></b>\nYour password was changed."
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if plain := s.telegramHTML("en", "password_changed", nil, ""); strings.Contains(plain, "<a") {
		t.Fatalf("link added without a URL: %q", plain)
	}
}

func TestTelegramHTMLEscapesUserText(t *testing.T) {
	s := testService(t)
	data := map[string]any{"show": `<script>&"x"`, "code": "S01E01", "name": `</a><b onclick="x">Evil</b>`}
	got := s.telegramHTML("en", "episode_aired", data, `https://tvt.example/e?a="1"&b=<2>`)
	for _, bad := range []string{"<script>", `</a><b`, `onclick="x"`, `"1"`, "<2>"} {
		if strings.Contains(got, bad) {
			t.Fatalf("unescaped %q in %q", bad, got)
		}
	}
	stripped := got
	for _, tag := range []string{"<b>", "</b>", "</a>"} {
		stripped = strings.ReplaceAll(stripped, tag, "")
	}
	stripped = strings.Replace(stripped, `<a href="https://tvt.example/e?a=&#34;1&#34;&amp;b=&lt;2&gt;">`, "", 1)
	if strings.ContainsAny(stripped, "<>") {
		t.Fatalf("stray markup after removing expected tags: %q", stripped)
	}
}

func TestArtworkPath(t *testing.T) {
	tvdb := "/tvdbimg/" + base64.RawURLEncoding.EncodeToString([]byte("/banners/ep.jpg"))
	cases := map[string]string{
		"":                     "",
		"/abc12345.jpg":        "/img/w780/abc12345.jpg",
		"tvdb:/banners/ep.jpg": tvdb,
	}
	for in, want := range cases {
		if got := artworkPath(in); got != want {
			t.Fatalf("artworkPath(%q) = %q, want %q", in, got, want)
		}
	}
	if got := firstNonEmpty("", "b", "c"); got != "b" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
}

type tgCall struct {
	method  string
	payload map[string]any
}

func fakeTelegram(t *testing.T, failPhoto bool) (*telegram.Bot, func() []tgCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []tgCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Errorf("bad payload: %v", err)
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		calls = append(calls, tgCall{method, p})
		mu.Unlock()
		if method == "sendPhoto" && failPhoto {
			w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: failed to get HTTP URL content"}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(srv.Close)
	b := telegram.New("123456:ABC-token", "tvt_bot", "s")
	b.API = srv.URL
	telegram.Use(b)
	return b, func() []tgCall { mu.Lock(); defer mu.Unlock(); return append([]tgCall(nil), calls...) }
}

func TestSendTelegramPhotoWithCaption(t *testing.T) {
	s := testService(t)
	var calls func() []tgCall
	s.Bot, calls = fakeTelegram(t, false)
	data := map[string]any{"show": "Show", "code": "S01E02", "name": "Pilot", "image": "/img/w780/still1234.jpg"}
	if err := s.sendTelegram(&store.User{TelegramChatID: "42"}, "en", "episode_aired", data, "https://tvt.example/episode/9"); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := calls()
	if len(got) != 1 || got[0].method != "sendPhoto" {
		t.Fatalf("calls = %+v, want one sendPhoto", got)
	}
	p := got[0].payload
	if p["photo"] != "https://tvt.example/img/w780/still1234.jpg" || p["parse_mode"] != "HTML" || !strings.Contains(p["caption"].(string), `<a href="https://tvt.example/episode/9">S01E02 Pilot</a>`) {
		t.Fatalf("payload = %+v", p)
	}
}

func TestSendTelegramFallsBackToTextWhenPhotoFails(t *testing.T) {
	s := testService(t)
	var calls func() []tgCall
	s.Bot, calls = fakeTelegram(t, true)
	data := map[string]any{"show": "Show", "code": "S01E02", "name": "Pilot", "image": "/img/w780/still1234.jpg"}
	if err := s.sendTelegram(&store.User{TelegramChatID: "42"}, "en", "episode_aired", data, "https://tvt.example/episode/9"); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := calls()
	if len(got) != 2 || got[0].method != "sendPhoto" || got[1].method != "sendMessage" {
		t.Fatalf("calls = %+v, want sendPhoto then sendMessage", got)
	}
	if got[1].payload["parse_mode"] != "HTML" || !strings.Contains(got[1].payload["text"].(string), "S01E02 Pilot</a>") {
		t.Fatalf("fallback payload = %+v", got[1].payload)
	}
}

func TestSendTelegramTextWithoutImage(t *testing.T) {
	s := testService(t)
	var calls func() []tgCall
	s.Bot, calls = fakeTelegram(t, false)
	if err := s.sendTelegram(&store.User{TelegramChatID: "42"}, "en", "password_changed", map[string]any{}, "https://tvt.example/settings"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := calls(); len(got) != 1 || got[0].method != "sendMessage" {
		t.Fatalf("calls = %+v, want one sendMessage", got)
	}
}
