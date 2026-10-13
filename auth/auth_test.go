package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tvt/db"
	"tvt/store"
)

func newSvc(t *testing.T) (*Service, *store.User) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(d)
	h, err := HashPassword("Correct-Horse9")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	u, err := st.CreateUser("a@example.com", "alice", h, "en", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return New(st, "0123456789abcdef0123456789abcdef", false), u
}

func TestLoginAndPasswordRules(t *testing.T) {
	s, _ := newSvc(t)
	if _, err := s.Login("a@example.com", "Correct-Horse9"); err != nil {
		t.Fatalf("good login: %v", err)
	}
	if _, err := s.Login("a@example.com", "nope nope"); err != ErrBadCredentials {
		t.Fatalf("bad password: %v", err)
	}
	if _, err := s.Login("ghost@example.com", "whatever1"); err != ErrBadCredentials {
		t.Fatalf("unknown user: %v", err)
	}
	if _, err := HashPassword("short"); err != ErrPasswordRules {
		t.Fatalf("short password accepted: %v", err)
	}
}

func TestPasswordStrengthRules(t *testing.T) {
	bad := []string{"alllowercase1!", "ALLUPPERCASE1!", "NoDigitsHere!", "NoSpecial123x", "Sh0rt!a", strings.Repeat("Aa1!", 19)}
	for _, p := range bad {
		if err := ValidPassword(p); err == nil {
			t.Errorf("%q should be rejected", p)
		}
	}
	for _, p := range []string{"Passw0rd!x", "Correct-Horse9", "Zz9_aaaa"} {
		if err := ValidPassword(p); err != nil {
			t.Errorf("%q should be accepted: %v", p, err)
		}
	}
}

func sessionOf(rr *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == CookieSession {
			return c
		}
	}
	return nil
}

func startSession(t *testing.T, s *Service, uid int64) string {
	t.Helper()
	rr := httptest.NewRecorder()
	w := s.WrapResponse(rr, httptest.NewRequest("GET", "/", nil))
	if err := s.StartSession(w, uid); err != nil {
		t.Fatalf("start: %v", err)
	}
	w.WriteHeader(200)
	c := sessionOf(rr)
	if c == nil {
		t.Fatalf("no session cookie issued")
	}
	if n := len(rr.Result().Cookies()); n != 1 {
		t.Fatalf("one cookie expected, got %d", n)
	}
	return BagField(c.Value, fieldRefresh)
}

func requestWithRefresh(refresh string) *http.Request {
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: bag{fieldRefresh: refresh}.encode()})
	return req
}

func authenticate(s *Service, req *http.Request) (*httptest.ResponseRecorder, error) {
	rr := httptest.NewRecorder()
	w := s.WrapResponse(rr, req)
	_, err := s.Authenticate(w, req)
	w.WriteHeader(200)
	return rr, err
}

func TestConcurrentRefreshRotationDoesNotLogOut(t *testing.T) {
	s, u := newSvc(t)
	refresh := startSession(t, s, u.ID)

	const n = 12
	var wg sync.WaitGroup
	results := make([]error, n)
	rotated := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec, err := authenticate(s, requestWithRefresh(refresh))
			results[i] = err
			if c := sessionOf(rec); c != nil {
				if r := BagField(c.Value, fieldRefresh); r != "" && r != refresh {
					rotated[i] = true
				}
			}
		}(i)
	}
	wg.Wait()
	winners := 0
	for i := 0; i < n; i++ {
		if results[i] != nil {
			t.Fatalf("request %d was logged out during rotation race: %v", i, results[i])
		}
		if rotated[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly 1 request to rotate the refresh token, got %d", winners)
	}
}

func TestSupersededTokenReuseOutsideGraceRevokesSessions(t *testing.T) {
	s, u := newSvc(t)
	refresh := startSession(t, s, u.ID)
	if _, err := authenticate(s, requestWithRefresh(refresh)); err != nil {
		t.Fatalf("first use: %v", err)
	}
	old := store.Ts(time.Now().Add(-2 * RotationGrace))
	if _, err := s.Store.DB.Exec(`UPDATE refresh_tokens SET superseded_at = ? WHERE superseded_at IS NOT NULL`, old); err != nil {
		t.Fatalf("age token: %v", err)
	}
	if _, err := authenticate(s, requestWithRefresh(refresh)); err != ErrBadToken {
		t.Fatalf("stale reuse should be rejected, got %v", err)
	}
	var left int
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id = ?`, u.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("all sessions should be revoked after reuse, left=%d err=%v", left, err)
	}
}

func TestEmailTokenSingleUse(t *testing.T) {
	s, u := newSvc(t)
	tok, err := s.IssueEmailToken(u.ID, PurposeReset)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := s.ConsumeEmailToken(tok, PurposeLogin); err != ErrBadToken {
		t.Fatalf("wrong purpose accepted: %v", err)
	}
	if uid, err := s.ConsumeEmailToken(tok, PurposeReset); err != nil || uid != u.ID {
		t.Fatalf("first consume: uid=%d err=%v", uid, err)
	}
	if _, err := s.ConsumeEmailToken(tok, PurposeReset); err != ErrBadToken {
		t.Fatalf("token reused: %v", err)
	}
}

func TestClientIPOnlyTrustsForwardedForFromLocalProxy(t *testing.T) {
	mk := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	cases := []struct{ remote, xff, want string }{
		{"198.51.100.7:4444", "203.0.113.9", "198.51.100.7"},
		{"198.51.100.7:4444", "", "198.51.100.7"},
		{"127.0.0.1:5555", "198.51.100.2, 203.0.113.9", "203.0.113.9"},
		{"10.1.2.3:5555", "203.0.113.9", "203.0.113.9"},
		{"127.0.0.1:5555", "not-an-ip", "127.0.0.1"},
		{"[::1]:5555", "2001:db8::1", "2001:db8::1"},
	}
	old := ClientIPHeader
	ClientIPHeader = "CF-Connecting-IP"
	r := mk("127.0.0.1:1", "")
	r.Header.Set("CF-Connecting-IP", "203.0.113.50")
	if got := ClientIP(r); got != "203.0.113.50" {
		t.Errorf("custom header: got %s", got)
	}
	ClientIPHeader = old
	for _, c := range cases {
		if got := ClientIP(mk(c.remote, c.xff)); got != c.want {
			t.Errorf("ClientIP(remote=%s xff=%q) = %s want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestAuthCookiesAreHttpOnly(t *testing.T) {
	s, u := newSvc(t)
	w := httptest.NewRecorder()
	if err := s.StartSession(w, u.ID); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	s.CSRFToken(w, r)
	for _, c := range w.Result().Cookies() {
		if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
			t.Errorf("cookie %s must be HttpOnly and SameSite=Lax: %+v", c.Name, c)
		}
	}
	if strings.Contains(w.Header().Get("Set-Cookie"), "password") {
		t.Errorf("cookies must not carry credentials")
	}
}

func sessionCookies(t *testing.T, s *Service, uid int64) (access, refresh string) {
	t.Helper()
	rr := httptest.NewRecorder()
	w := s.WrapResponse(rr, httptest.NewRequest("GET", "/", nil))
	if err := s.StartSession(w, uid); err != nil {
		t.Fatal(err)
	}
	w.WriteHeader(200)
	c := sessionOf(rr)
	return BagField(c.Value, fieldAccess), BagField(c.Value, fieldRefresh)
}

func requestWith(access, refresh string) *http.Request {
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: bag{fieldAccess: access, fieldRefresh: refresh}.encode()})
	return req
}

func TestAccessTokenDiesWithItsSession(t *testing.T) {
	s, u := newSvc(t)
	access, refresh := sessionCookies(t, s, u.ID)
	if _, err := authenticate(s, requestWith(access, refresh)); err != nil {
		t.Fatalf("fresh session rejected: %v", err)
	}
	if err := s.RevokeAll(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticate(s, requestWith(access, "")); err != ErrBadToken {
		t.Fatalf("a still-unexpired access token must stop working once sessions are revoked: %v", err)
	}

	access2, refresh2 := sessionCookies(t, s, u.ID)
	otherAccess, otherRefresh := sessionCookies(t, s, u.ID)
	req := requestWith(access2, refresh2)
	if err := s.EndSession(httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticate(s, requestWith(access2, "")); err != ErrBadToken {
		t.Fatalf("logout must end that session's access token immediately: %v", err)
	}
	if _, err := authenticate(s, requestWith(otherAccess, otherRefresh)); err != nil {
		t.Fatalf("logging out one device must not end another: %v", err)
	}
}

func startSessionFrom(t *testing.T, s *Service, uid int64, agent string) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("User-Agent", agent)
	rr := httptest.NewRecorder()
	w := s.WrapResponse(rr, req)
	if err := s.StartSessionFor(w, req, uid); err != nil {
		t.Fatalf("start: %v", err)
	}
	w.WriteHeader(200)
	return BagField(sessionOf(rr).Value, fieldRefresh)
}

func requestFrom(refresh, agent string) *http.Request {
	req := requestWithRefresh(refresh)
	req.Header.Set("User-Agent", agent)
	return req
}

func TestRefreshTokenThatNeverArrivedHealsFromTheSameBrowser(t *testing.T) {
	s, u := newSvc(t)
	first := startSessionFrom(t, s, u.ID, "Phone/1.0")
	if _, err := authenticate(s, requestFrom(first, "Phone/1.0")); err != nil {
		t.Fatalf("first rotation: %v", err)
	}
	old := store.Ts(time.Now().Add(-10 * time.Minute))
	if _, err := s.Store.DB.Exec(`UPDATE refresh_tokens SET superseded_at = ? WHERE superseded_at IS NOT NULL`, old); err != nil {
		t.Fatal(err)
	}
	rr, err := authenticate(s, requestFrom(first, "Phone/1.0"))
	if err != nil {
		t.Fatalf("the same browser presenting the token whose replacement never arrived is not logged out: %v", err)
	}
	c := sessionOf(rr)
	if c == nil || BagField(c.Value, fieldRefresh) == "" || BagField(c.Value, fieldRefresh) == first {
		t.Fatalf("it gets a fresh refresh token")
	}
	var left int
	s.Store.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id = ? AND superseded_at IS NULL`, u.ID).Scan(&left)
	if left != 1 {
		t.Fatalf("exactly one live token remains: %d", left)
	}
	if _, err := authenticate(s, requestFrom(BagField(c.Value, fieldRefresh), "Phone/1.0")); err != nil {
		t.Fatalf("and the new token works: %v", err)
	}
}

func TestReplayFromAnotherBrowserStillRevokesEverything(t *testing.T) {
	s, u := newSvc(t)
	first := startSessionFrom(t, s, u.ID, "Phone/1.0")
	authenticate(s, requestFrom(first, "Phone/1.0"))
	old := store.Ts(time.Now().Add(-10 * time.Minute))
	s.Store.DB.Exec(`UPDATE refresh_tokens SET superseded_at = ? WHERE superseded_at IS NOT NULL`, old)
	if _, err := authenticate(s, requestFrom(first, "Thief/9.9")); err != ErrBadToken {
		t.Fatalf("a replay from a different browser is theft: %v", err)
	}
	var left int
	s.Store.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id = ?`, u.ID).Scan(&left)
	if left != 0 {
		t.Fatalf("all sessions revoked: %d", left)
	}
}
