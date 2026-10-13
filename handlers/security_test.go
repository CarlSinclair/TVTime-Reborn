package handlers_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"tvt/auth"
	"tvt/store"
	"tvt/trakt"
	"tvt/tvmaze"

	"github.com/pquerna/otp/totp"
)

func (e *env) mustUser(t *testing.T, name string) *store.User {
	u, err := e.store.UserByUsername(name)
	if err != nil {
		t.Fatalf("user %s: %v", name, err)
	}
	return u
}

func TestTwoFactorFlowAndReplayProtection(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "erin", true)
	_, out := c.post("/api/account/2fa/setup", nil)
	secret, _ := out["secret"].(string)
	if secret == "" || !strings.HasPrefix(out["qr"].(string), "data:image/png;base64,") {
		t.Fatalf("setup response: %v", out)
	}
	if code, out := c.post("/api/account/2fa/enable", map[string]string{"code": "000000"}); code != 400 {
		t.Fatalf("bad enable code accepted: %d %v", code, out)
	}
	now := time.Now()
	code1, _ := totp.GenerateCode(secret, now)
	code, out := c.post("/api/account/2fa/enable", map[string]string{"code": code1})
	if code != 200 {
		t.Fatalf("enable: %d %v", code, out)
	}
	codes, _ := out["recovery_codes"].([]any)
	if len(codes) != 10 {
		t.Fatalf("expected 10 recovery codes, got %d", len(codes))
	}

	login := newClient(t, e.srv.URL)
	code, out = login.post("/api/auth/login", map[string]string{"email": "erin@example.com", "password": "Passw0rd!x1"})
	if code != 200 || out["mfa"] != true {
		t.Fatalf("login should require mfa: %d %v", code, out)
	}
	if status, _ := login.get("/shows"); status != 303 {
		t.Fatalf("no session may exist before the second factor, got %d", status)
	}
	if code, _ := login.post("/api/auth/2fa", map[string]string{"code": code1}); code != 401 {
		t.Fatalf("a TOTP code used during setup must not be replayable, got %d", code)
	}
	next, _ := totp.GenerateCode(secret, now.Add(30*time.Second))
	if code, out := login.post("/api/auth/2fa", map[string]string{"code": next}); code != 200 {
		t.Fatalf("valid second factor rejected: %d %v", code, out)
	}
	if status, _ := login.get("/shows"); status != 200 {
		t.Fatalf("session should exist after 2fa, got %d", status)
	}

	again := newClient(t, e.srv.URL)
	again.post("/api/auth/login", map[string]string{"email": "erin@example.com", "password": "Passw0rd!x1"})
	rc := codes[0].(string)
	if code, out := again.post("/api/auth/2fa", map[string]string{"code": rc}); code != 200 {
		t.Fatalf("recovery code rejected: %d %v", code, out)
	}
	third := newClient(t, e.srv.URL)
	third.post("/api/auth/login", map[string]string{"email": "erin@example.com", "password": "Passw0rd!x1"})
	if code, _ := third.post("/api/auth/2fa", map[string]string{"code": rc}); code != 401 {
		t.Fatalf("a recovery code must work only once, got %d", code)
	}
}

func TestMFAPendingCookieIsNotASession(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "fred", true)
	_, out := c.post("/api/account/2fa/setup", nil)
	code, _ := totp.GenerateCode(out["secret"].(string), time.Now())
	c.post("/api/account/2fa/enable", map[string]string{"code": code})
	fresh := newClient(t, e.srv.URL)
	if code, _ := fresh.post("/api/auth/2fa", map[string]string{"code": "123456"}); code != 401 {
		t.Fatalf("2fa endpoint without a pending login must fail, got %d", code)
	}
}

func tgPayload(token string, fields map[string]string) url.Values {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+fields[k])
	}
	secret := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(parts, "\n")))
	v := url.Values{}
	for k, val := range fields {
		v.Set(k, val)
	}
	v.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return v
}

func TestTelegramLoginCreatesAccountAndRejectsForgery(t *testing.T) {
	e := newEnv(t)
	c := newClient(t, e.srv.URL)
	fresh := func(id string) url.Values {
		return tgPayload("123456:test-token", map[string]string{"id": id, "first_name": "Tess", "username": "tess_tg", "auth_date": strconv.FormatInt(time.Now().Unix(), 10)})
	}
	bad := fresh("77")
	bad.Set("id", "78")
	if status, _ := c.get("/auth/telegram?" + bad.Encode()); status != 303 {
		t.Fatalf("forged payload status %d", status)
	}
	if _, err := e.store.UserByTelegramID(78); err == nil {
		t.Fatalf("a forged Telegram payload created an account")
	}
	if status, _ := c.get("/auth/telegram?" + fresh("77").Encode()); status != 303 {
		t.Fatalf("telegram login status %d", status)
	}
	u, err := e.store.UserByTelegramID(77)
	if err != nil {
		t.Fatalf("telegram account not created: %v", err)
	}
	if u.Username != "tess_tg" || u.HasPassword() || u.Email != "" || !u.CanComment() {
		t.Fatalf("unexpected telegram account: %+v", u)
	}
	if status, _ := c.get("/shows"); status != 200 {
		t.Fatalf("telegram login should open a session, got %d", status)
	}
	if code, out := c.post("/api/account/telegram/unlink", nil); code != 400 || out["key"] != "err.last_login_method" {
		t.Fatalf("must not strand a telegram-only account: %d %v", code, out)
	}
}

func TestTelegramBotLinkingAndNotificationDelivery(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "gina", true)
	code, out := c.post("/api/account/telegram/link-token", nil)
	if code != 200 {
		t.Fatalf("link token: %d %v", code, out)
	}
	link := out["url"].(string)
	token := link[strings.Index(link, "start=")+len("start="):]

	webhook := func(chat int64, text string, secret string) int {
		body := fmt.Sprintf(`{"update_id":1,"message":{"message_id":1,"text":%q,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"language_code":"en"}}}`, text, chat, chat)
		req, _ := http.NewRequest("POST", e.srv.URL+"/api/telegram/webhook", strings.NewReader(body))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if webhook(555, "/start "+token, "wrong") != 403 {
		t.Fatalf("webhook accepted a wrong secret")
	}
	if webhook(555, "/start "+token, "whsecret") != 200 {
		t.Fatalf("webhook rejected the right secret")
	}
	u := e.mustUser(t, "gina")
	if u.TelegramChatID != "555" {
		t.Fatalf("chat not linked: %q", u.TelegramChatID)
	}
	if webhook(556, "/start "+token, "whsecret") != 200 {
		t.Fatalf("second use of token returned an error status")
	}
	if e.mustUser(t, "gina").TelegramChatID != "555" {
		t.Fatalf("a used link token linked another chat")
	}

	e.nt.Send(u.ID, "episode_aired", "/episode/1", map[string]any{"show": "Fake Show", "code": "S01E01", "name": "Pilot"})
	var delivered bool
	for _, m := range e.tg.messages() {
		if m["chat_id"] == "555" && strings.Contains(fmt.Sprint(m["text"]), "Fake Show") {
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("episode alert not delivered over Telegram: %v", e.tg.messages())
	}

	if err := e.store.SetTelegramChat(u.ID, "666"); err != nil {
		t.Fatal(err)
	}
	e.nt.Send(u.ID, "episode_aired", "/episode/1", map[string]any{"show": "X", "code": "S01E02", "name": "Y"})
	if e.mustUser(t, "gina").TelegramChatID != "" {
		t.Fatalf("a chat that blocked the bot should be dropped")
	}
}

func TestAdminCannotBeClaimedByRegisteringFirst(t *testing.T) {
	e := newEnv(t)
	e.cfg.AdminEmail = "boss@example.com"
	c := newClient(t, e.srv.URL)
	if code, out := c.post("/api/auth/register", map[string]string{"email": "boss@example.com", "username": "imposter", "password": "Passw0rd!x1"}); code != 200 {
		t.Fatalf("register: %d %v", code, out)
	}
	if e.mustUser(t, "imposter").IsAdmin {
		t.Fatalf("registering the admin email without verifying it granted admin")
	}
	tok, err := e.auth.IssueEmailToken(e.mustUser(t, "imposter").ID, "verify")
	if err != nil {
		t.Fatal(err)
	}
	owner := newClient(t, e.srv.URL)
	if code, out := owner.post("/api/auth/verify/consume", map[string]string{"token": tok}); code != 200 {
		t.Fatalf("verify: %d %v", code, out)
	}
	if !e.mustUser(t, "imposter").IsAdmin {
		t.Fatalf("verified admin email should be promoted")
	}
}

func TestMagicLinkClaimsUnverifiedAccountAndWipesRegistrantAccess(t *testing.T) {
	e := newEnv(t)
	attacker := newClient(t, e.srv.URL)
	attacker.post("/api/auth/register", map[string]string{"email": "victim@example.com", "username": "squatter", "password": "Attack3r!pass1"})
	if status, _ := attacker.get("/settings"); status != 200 {
		t.Fatalf("setup: attacker should be signed in")
	}
	u := e.mustUser(t, "squatter")
	tok, _ := e.auth.IssueEmailToken(u.ID, "login")
	victim := newClient(t, e.srv.URL)
	if code, out := victim.post("/api/auth/magic/consume", map[string]string{"token": tok}); code != 200 {
		t.Fatalf("magic consume: %d %v", code, out)
	}
	if status, _ := victim.get("/settings"); status != 200 {
		t.Fatalf("the email owner should be signed in")
	}
	if e.mustUser(t, "squatter").HasPassword() {
		t.Fatalf("the squatter's password survived the owner claiming the account")
	}
	again := newClient(t, e.srv.URL)
	if code, _ := again.post("/api/auth/login", map[string]string{"email": "victim@example.com", "password": "Attack3r!pass1"}); code != 401 {
		t.Fatalf("squatter can still log in with the old password")
	}
}

func TestReservedUsernamesRefused(t *testing.T) {
	e := newEnv(t)
	c := newClient(t, e.srv.URL)
	if code, _ := c.post("/api/auth/register", map[string]string{"email": "a@example.com", "username": "Admin", "password": "Passw0rd!x1"}); code != 409 {
		t.Fatalf("reserved username accepted: %d", code)
	}
}

func TestPasskeyEndpoints(t *testing.T) {
	e := newEnv(t)
	anon := newClient(t, e.srv.URL)
	code, out := anon.post("/api/auth/passkey/begin", nil)
	if code != 200 {
		t.Fatalf("login begin: %d %v", code, out)
	}
	opts := out["options"].(map[string]any)["publicKey"].(map[string]any)
	if opts["challenge"] == "" || opts["userVerification"] != "required" {
		t.Fatalf("passkey login must require user verification: %v", opts)
	}
	if code, _ := anon.post("/api/auth/passkey/finish", map[string]string{"id": "x"}); code != 401 && code != 400 {
		t.Fatalf("garbage assertion accepted: %d", code)
	}
	if code, _ := anon.post("/api/account/passkey/begin", nil); code != 401 {
		t.Fatalf("adding a passkey needs a session, got %d", code)
	}
	c := e.signup(t, "hana", true)
	code, out = c.post("/api/account/passkey/begin", nil)
	if code != 200 {
		t.Fatalf("add begin: %d %v", code, out)
	}
	create := out["options"].(map[string]any)["publicKey"].(map[string]any)
	sel := create["authenticatorSelection"].(map[string]any)
	if sel["residentKey"] != "required" || sel["userVerification"] != "required" {
		t.Fatalf("passkeys must be discoverable and user-verified: %v", sel)
	}
	if code, _ := c.post("/api/account/passkey/finish?name=x", map[string]string{"id": "x"}); code != 401 && code != 400 {
		t.Fatalf("garbage attestation accepted: %d", code)
	}
}

func TestPasskeyLastMethodGuard(t *testing.T) {
	e := newEnv(t)
	c := newClient(t, e.srv.URL)
	if status, _ := c.get("/auth/telegram?" + tgPayload("123456:test-token", map[string]string{"id": "91", "username": "solo", "first_name": "S", "auth_date": strconv.FormatInt(time.Now().Unix(), 10)}).Encode()); status != 303 {
		t.Fatalf("telegram login status %d", status)
	}
	u, _ := e.store.UserByTelegramID(91)
	if err := e.store.AddPasskey(u.ID, []byte("cred-1"), "{}", "k"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.UnlinkTelegram(u.ID); err != nil {
		t.Fatal(err)
	}
	rows, _ := e.store.Passkeys(u.ID)
	if code, out := c.post(fmt.Sprintf("/api/account/passkey/%d/delete", rows[0].ID), nil); code != 400 || out["key"] != "err.passkey_last_method" {
		t.Fatalf("deleting the only sign-in method must be refused: %d %v", code, out)
	}
}

func TestNotificationsPreferencesAndPages(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "ivy", true)
	u := e.mustUser(t, "ivy")
	e.nt.Send(u.ID, "episode_aired", "/episode/1", map[string]any{"show": "Fake Show", "code": "S01E01", "name": "Pilot"})
	n, _ := e.store.UnreadCount(u.ID)
	if n != 1 {
		t.Fatalf("expected an in-app notification, unread=%d", n)
	}
	_, raw := c.get("/api/notifications/recent")
	if !strings.Contains(raw, "New episode: Fake Show") {
		t.Fatalf("notification not rendered: %s", raw)
	}
	if code, out := c.post("/api/notifications/settings", map[string]any{
		"master": map[string]bool{"in_app": true, "push": false, "email": false, "telegram": false},
		"prefs":  map[string]map[string]bool{"episodes": {"in_app": false}},
	}); code != 200 {
		t.Fatalf("save settings: %d %v", code, out)
	}
	e.nt.Send(u.ID, "episode_aired", "/episode/2", map[string]any{"show": "Fake Show", "code": "S01E02", "name": "Two"})
	if n, _ := e.store.UnreadCount(u.ID); n != 1 {
		t.Fatalf("a disabled category still produced a notification, unread=%d", n)
	}
	e.nt.Send(u.ID, "password_changed", "/settings", nil)
	if n, _ := e.store.UnreadCount(u.ID); n != 2 {
		t.Fatalf("security alerts must ignore preferences, unread=%d", n)
	}
	if code, out := c.post("/api/notifications/test", map[string]string{"channel": "telegram"}); code != 400 || out["key"] != "err.notify_no_telegram" {
		t.Fatalf("telegram test without a link: %d %v", code, out)
	}
	if status, body := c.get("/notifications"); status != 200 || !strings.Contains(body, "Password changed") {
		t.Fatalf("notifications page: %d", status)
	}
	for _, p := range []string{"/settings?tab=account", "/settings?tab=profile", "/settings?tab=notifications"} {
		if status, _ := c.get(p); status != 200 {
			t.Fatalf("%s status %d", p, status)
		}
	}
}

func TestNewEpisodeAlertsAreSentOncePerEpisode(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "joan", true)
	c.post("/api/show/555/follow", map[string]string{"status": "watching"})
	u := e.mustUser(t, "joan")
	e.nt.EpisodeAlerts(time.Date(2020, 1, 9, 12, 0, 0, 0, time.UTC))
	first, _ := e.store.UnreadCount(u.ID)
	if first != 1 {
		t.Fatalf("expected one alert (the episode inside the 2-day window; the older one must not flood), got %d", first)
	}
	e.nt.EpisodeAlerts(time.Date(2020, 1, 9, 13, 0, 0, 0, time.UTC))
	if again, _ := e.store.UnreadCount(u.ID); again != first {
		t.Fatalf("alerts repeated: %d -> %d", first, again)
	}
}

func TestProfileStatsAndPrivacy(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "kai", true)
	other := e.signup(t, "lou", true)
	c.post("/api/show/555/follow", map[string]string{"status": "watching"})
	c.post("/api/show/555/favorite", map[string]bool{"favorite": true})
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	c.post(fmt.Sprintf("/api/episode/%d/watch", eps[0].ID), map[string]bool{"watched": true})
	u := e.mustUser(t, "kai")
	st, err := e.store.WatchStats(u.ID, time.UTC, time.Now())
	if err != nil || st.Episodes != 1 || st.Lifetime != 40 || st.Today != 40 || st.Week != 40 || st.Month != 40 {
		t.Fatalf("stats: %+v err=%v", st, err)
	}
	g, _ := e.store.FavoriteGenres(u.ID, 5)
	if len(g) == 0 || g[0].Name != "Drama" || g[0].Minutes != 40 {
		t.Fatalf("favorite genres: %+v", g)
	}
	if status, body := other.get("/u/kai"); status != 200 || !strings.Contains(body, "Fake Show") || !strings.Contains(body, "Drama") {
		t.Fatalf("public profile missing favorites/genres: %d", status)
	}
	c.post("/api/profile", map[string]any{"display_name": "Kai", "bio": "hi", "public": false})
	if _, body := other.get("/u/kai"); strings.Contains(body, "Fake Show") {
		t.Fatalf("a private profile leaked favorites")
	}
	if status, body := c.get("/u/kai"); status != 200 || !strings.Contains(body, "Fake Show") {
		t.Fatalf("owner must still see their private profile: %d", status)
	}
}

func TestLocalizedCatalogAndRTL(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "mia", true)
	if code, _ := c.post("/api/account/locale", map[string]string{"locale": "ar"}); code != 200 {
		t.Fatalf("set locale")
	}
	_, body := c.get("/")
	if !strings.Contains(body, `lang="ar"`) || !strings.Contains(body, `dir="rtl"`) {
		t.Fatalf("arabic pages must be rtl")
	}
	for _, l := range []string{"es", "it", "fr", "zh", "ru", "ja", "ko", "pt", "en"} {
		c.post("/api/account/locale", map[string]string{"locale": l})
		if status, b := c.get("/settings?tab=notifications"); status != 200 || !strings.Contains(b, fmt.Sprintf(`lang="%s"`, l)) {
			t.Fatalf("locale %s did not render", l)
		}
	}
}

func mustURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}

func TestAlertTimingFallsBackToMiddayWithoutAnExactTime(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "nora", true)
	c.post("/api/show/555/follow", map[string]string{"status": "watching"})
	u := e.mustUser(t, "nora")
	unread := func() int { n, _ := e.store.UnreadCount(u.ID); return n }
	if code, out := c.post("/api/notifications/timing", map[string]any{"offset_min": -30}); code != 200 {
		t.Fatalf("timing: %d %v", code, out)
	}
	e.nt.EpisodeAlerts(time.Date(2020, 1, 8, 11, 0, 0, 0, time.UTC))
	if unread() != 0 {
		t.Fatalf("alert fired before midday - 30m on the air date")
	}
	e.nt.EpisodeAlerts(time.Date(2020, 1, 8, 11, 45, 0, 0, time.UTC))
	if unread() != 1 {
		t.Fatalf("alert did not fire 30 minutes before the midday anchor")
	}
	e.nt.EpisodeAlerts(time.Date(2020, 1, 8, 20, 0, 0, 0, time.UTC))
	if unread() != 1 {
		t.Fatalf("alert repeated")
	}
}

func TestAlertTimingDayAfterAndStaleGuard(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "otto", true)
	c.post("/api/show/555/follow", map[string]string{"status": "watching"})
	u := e.mustUser(t, "otto")
	unread := func() int { n, _ := e.store.UnreadCount(u.ID); return n }
	c.post("/api/notifications/timing", map[string]any{"offset_min": 1440})
	e.nt.EpisodeAlerts(time.Date(2020, 1, 9, 6, 0, 0, 0, time.UTC))
	if unread() != 0 {
		t.Fatalf("day-after alert fired before the next midday")
	}
	e.nt.EpisodeAlerts(time.Date(2020, 1, 9, 13, 0, 0, 0, time.UTC))
	if unread() != 1 {
		t.Fatalf("day-after alert did not fire the next day")
	}

	c2 := e.signup(t, "pia", true)
	c2.post("/api/show/555/follow", map[string]string{"status": "watching"})
	p := e.mustUser(t, "pia")
	e.nt.EpisodeAlerts(time.Date(2020, 3, 1, 12, 0, 0, 0, time.UTC))
	if n, _ := e.store.UnreadCount(p.ID); n != 0 {
		t.Fatalf("episodes months old must not trigger alerts, got %d", n)
	}
}

func TestAlertTimingValidation(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "quin", true)
	for _, bad := range []map[string]any{{"offset_min": 20000}, {"offset_min": -20000}} {
		if code, _ := c.post("/api/notifications/timing", bad); code != 400 {
			t.Fatalf("invalid timing accepted: %v -> %d", bad, code)
		}
	}
	if code, _ := c.post("/api/notifications/timing", map[string]any{"offset_min": -10080}); code != 200 {
		t.Fatalf("the 7-day boundary should be accepted")
	}
	status, body := c.get("/settings?tab=notifications")
	if status != 200 || !strings.Contains(body, `id="timing-box"`) {
		t.Fatalf("timing panel missing: %d", status)
	}
	if strings.Contains(body, `id="timing-time"`) {
		t.Fatalf("users must not be asked for a release time")
	}
}

func fakeTVmaze(t *testing.T, found bool, withTime bool) *tvmaze.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/lookup/shows", func(w http.ResponseWriter, r *http.Request) {
		if !found {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"id":9}`))
	})
	mux.HandleFunc("/shows/9/episodes", func(w http.ResponseWriter, r *http.Request) {
		airtime, stamp2, stamp1 := "21:00", "2020-01-09T02:00:00+00:00", "2020-01-02T02:00:00+00:00"
		if !withTime {
			airtime, stamp2, stamp1 = "", "2020-01-08T12:00:00+00:00", "2020-01-01T12:00:00+00:00"
		}
		fmt.Fprintf(w, `[{"season":1,"number":1,"airdate":"2020-01-01","airtime":%q,"airstamp":%q},{"season":1,"number":2,"airdate":"2020-01-08","airtime":%q,"airstamp":%q},{"season":0,"number":null,"airdate":"2020-01-03","airtime":"","airstamp":"2020-01-03T12:00:00+00:00"}]`, airtime, stamp1, airtime, stamp2)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := tvmaze.New()
	c.Base = srv.URL
	return c
}

func TestExactAirTimeFromTVmazeDrivesAlerts(t *testing.T) {
	e := newEnv(t)
	e.sync.TVmaze = fakeTVmaze(t, true, true)
	c := e.signup(t, "rhea", true)
	c.post("/api/show/555/follow", map[string]string{"status": "watching"})
	u := e.mustUser(t, "rhea")
	sh, _ := e.store.ShowByTMDB(555)
	ep, err := e.store.EpisodeByNumber(sh.ID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := e.store.Episode(ep.ID, 0)
	if full.AirAt != "2020-01-09 02:00:00" {
		t.Fatalf("TVmaze broadcast time not stored (UTC): %q", full.AirAt)
	}
	if status, body := c.get(fmt.Sprintf("/episode/%d", ep.ID)); status != 200 || !strings.Contains(body, `data-ts="2020-01-09 02:00:00"`) {
		t.Fatalf("episode page should expose the exact air time: %d", status)
	}
	unread := func() int { n, _ := e.store.UnreadCount(u.ID); return n }
	e.nt.EpisodeAlerts(time.Date(2020, 1, 9, 1, 0, 0, 0, time.UTC))
	if unread() != 0 {
		t.Fatalf("alert fired before the exact broadcast instant")
	}
	e.nt.EpisodeAlerts(time.Date(2020, 1, 9, 2, 5, 0, 0, time.UTC))
	if unread() != 1 {
		t.Fatalf("alert did not fire at the exact broadcast instant")
	}
}

func TestPlaceholderAirstampIsNotTrusted(t *testing.T) {
	e := newEnv(t)
	e.sync.TVmaze = fakeTVmaze(t, true, false)
	c := e.signup(t, "sol", true)
	c.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh, _ := e.store.ShowByTMDB(555)
	ep, _ := e.store.EpisodeByNumber(sh.ID, 1, 2)
	full, _ := e.store.Episode(ep.ID, 0)
	if full.AirAt != "" {
		t.Fatalf("a placeholder 12:00Z stamp (no airtime) was stored as a real air time: %q", full.AirAt)
	}
}

func TestShowSyncsWithoutTVmazeMatch(t *testing.T) {
	e := newEnv(t)
	e.sync.TVmaze = fakeTVmaze(t, false, true)
	c := e.signup(t, "tess2", true)
	if code, out := c.post("/api/show/555/follow", map[string]string{"status": "watching"}); code != 200 {
		t.Fatalf("a missing TVmaze match must not break following: %d %v", code, out)
	}
	sh, _ := e.store.ShowByTMDB(555)
	id, checked, _ := e.store.ProviderState(sh.ID, store.SourceTVmaze)
	if id != 0 || checked == "" {
		t.Fatalf("a miss should be remembered so it is not retried constantly: id=%d checked=%q", id, checked)
	}
}

func fakeTrakt(t *testing.T, mode string, headers *[]http.Header) *trakt.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/search/tmdb/555", func(w http.ResponseWriter, r *http.Request) {
		*headers = append(*headers, r.Header.Clone())
		w.Write([]byte(`[{"type":"show","show":{"ids":{"trakt":42}}}]`))
	})
	mux.HandleFunc("/shows/42/seasons", func(w http.ResponseWriter, r *http.Request) {
		*headers = append(*headers, r.Header.Clone())
		e1, e2 := "2020-01-01T21:30:00.000Z", "2020-01-08T21:30:00.000Z"
		if mode == "placeholder" {
			e1, e2 = "2020-01-01T00:00:00.000Z", "2020-01-08T12:00:00.000Z"
		}
		fmt.Fprintf(w, `[{"number":1,"episodes":[{"season":1,"number":1,"first_aired":%q},{"season":1,"number":2,"first_aired":%q}]}]`, e1, e2)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := trakt.New("client-id-123")
	c.Base = srv.URL
	return c
}

func TestTraktFillsGapsWhenTVmazeHasNoTime(t *testing.T) {
	e := newEnv(t)
	var hdr []http.Header
	e.sync.TVmaze = fakeTVmaze(t, false, true)
	e.sync.Trakt = fakeTrakt(t, "real", &hdr)
	e.sync.Now = func() time.Time { return time.Date(2020, 1, 9, 0, 0, 0, 0, time.UTC) }
	c := e.signup(t, "trent", true)
	c.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh, _ := e.store.ShowByTMDB(555)
	ep, _ := e.store.EpisodeByNumber(sh.ID, 1, 2)
	full, _ := e.store.Episode(ep.ID, 0)
	if full.AirAt != "2020-01-08 21:30:00" {
		t.Fatalf("Trakt fallback time not stored: %q", full.AirAt)
	}
	if len(hdr) == 0 || hdr[0].Get("trakt-api-key") != "client-id-123" || hdr[0].Get("trakt-api-version") != "2" {
		t.Fatalf("Trakt requests must carry the client id and API version: %v", hdr)
	}
	var src string
	if err := e.store.DB.QueryRow(`SELECT air_source FROM episodes WHERE id = ?`, ep.ID).Scan(&src); err != nil || src != "trakt" {
		t.Fatalf("source should be recorded: %q %v", src, err)
	}
}

func TestTraktNeverOverridesTVmazeAndRejectsPlaceholders(t *testing.T) {
	e := newEnv(t)
	var hdr []http.Header
	e.sync.TVmaze = fakeTVmaze(t, true, true)
	e.sync.Trakt = fakeTrakt(t, "real", &hdr)
	e.sync.Now = func() time.Time { return time.Date(2020, 1, 9, 0, 0, 0, 0, time.UTC) }
	c := e.signup(t, "ursa", true)
	c.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh, _ := e.store.ShowByTMDB(555)
	ep, _ := e.store.EpisodeByNumber(sh.ID, 1, 2)
	full, _ := e.store.Episode(ep.ID, 0)
	if full.AirAt != "2020-01-09 02:00:00" {
		t.Fatalf("TVmaze's exact time must win over Trakt: %q", full.AirAt)
	}
	if len(hdr) != 0 {
		t.Fatalf("Trakt should not even be called when TVmaze already timed every episode")
	}

	e2 := newEnv(t)
	var hdr2 []http.Header
	e2.sync.TVmaze = fakeTVmaze(t, false, true)
	e2.sync.Trakt = fakeTrakt(t, "placeholder", &hdr2)
	e2.sync.Now = func() time.Time { return time.Date(2020, 1, 9, 0, 0, 0, 0, time.UTC) }
	c2 := e2.signup(t, "vera", true)
	c2.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh2, _ := e2.store.ShowByTMDB(555)
	ep2, _ := e2.store.EpisodeByNumber(sh2.ID, 1, 2)
	full2, _ := e2.store.Episode(ep2.ID, 0)
	if full2.AirAt != "" {
		t.Fatalf("a midnight/noon Trakt stamp is a placeholder and must not be stored: %q", full2.AirAt)
	}
}

func TestNoThirdPartyContactOnPageLoad(t *testing.T) {
	e := newEnv(t)
	signed := e.signup(t, "wynn", true)
	anon := newClient(t, e.srv.URL)
	ext := regexp.MustCompile(`(?i)(src|href)="https?://[^"]*"`)
	allowedLinks := []string{"themoviedb.org", "tvmaze.com", "trakt.tv", "github.com/CarlSinclair/tvt", "carl-sinclair.com"}
	pages := map[string]*client{
		"/": signed, "/login": anon, "/register": anon, "/forgot": anon, "/privacy": anon, "/about": anon,
		"/settings?tab=account": signed, "/settings?tab=notifications": signed, "/search?q=fake": signed, "/show/555": signed,
	}
	for p, c := range pages {
		status, body := c.get(p)
		if status != 200 {
			t.Errorf("%s: status %d", p, status)
			continue
		}
		if strings.Contains(strings.ToLower(body), "telegram.org") {
			t.Errorf("%s references telegram.org on load; the script must load only after a click", p)
		}
		for _, m := range ext.FindAllString(body, -1) {
			lower := strings.ToLower(m)
			if strings.HasPrefix(lower, "src=") {
				t.Errorf("%s loads an external resource: %s", p, m)
				continue
			}
			ok := false
			for _, a := range allowedLinks {
				if strings.Contains(lower, strings.ToLower(a)) {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s links to an unexpected external site: %s", p, m)
			}
		}
	}
	resp, err := http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data:") || strings.Contains(csp, "image.tmdb.org") {
		t.Fatalf("images must be same-origin only: %s", csp)
	}
}

func TestPrivacyPageAndAGPLSourceLink(t *testing.T) {
	e := newEnv(t)
	e.cfg.OperatorName = "Example Operator"
	e.cfg.PrivacyContact = "privacy@example.org"
	e.cfg.SourceURL = "https://example.org/source"
	c := newClient(t, e.srv.URL)
	status, body := c.get("/privacy")
	if status != 200 || !strings.Contains(body, "Privacy policy") || !strings.Contains(body, "Example Operator") || !strings.Contains(body, "privacy@example.org") {
		t.Fatalf("privacy page: %d", status)
	}
	if !strings.Contains(body, `href="https://example.org/source"`) {
		t.Fatalf("the AGPL requires a source link on every page")
	}
	for _, l := range []string{"pt", "es", "it", "fr", "zh", "ru", "ja", "ko", "ar"} {
		req, _ := http.NewRequest("GET", e.srv.URL+"/privacy", nil)
		req.Header.Set("Accept-Language", l+",en;q=0.5")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		b := string(raw)
		if resp.StatusCode != 200 || strings.Contains(b, "Privacy policy") || !strings.Contains(b, `lang="`+l+`"`) {
			t.Fatalf("privacy page not localized for %s (status %d)", l, resp.StatusCode)
		}
	}
}

func TestPasskeyUIHiddenWhenWebAuthnUnavailable(t *testing.T) {
	e := newEnv(t)
	anon := newClient(t, e.srv.URL)
	if _, body := anon.get("/login"); !strings.Contains(body, `id="passkey-login"`) {
		t.Fatalf("passkey button should show when WebAuthn is configured")
	}
	if code, _ := anon.post("/api/auth/passkey/begin", nil); code != 200 {
		t.Fatalf("begin should work when enabled, got %d", code)
	}
	disabled := auth.New(e.store, e.cfg.JWTSecret, false)
	if disabled.PasskeysEnabled() {
		t.Fatalf("a service without EnableWebAuthn must report passkeys as disabled")
	}
	if err := disabled.EnableWebAuthn("127.0.0.1", "http://127.0.0.1", "TVT"); err == nil {
		t.Fatalf("an IP address is not a valid relying-party id and must be refused, not crash")
	}
}
