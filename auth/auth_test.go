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
	h, err := HashPassword("correct horse")
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
	if _, err := s.Login("a@example.com", "correct horse"); err != nil {
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

func cookiesOf(w *httptest.ResponseRecorder) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, c := range w.Result().Cookies() {
		out[c.Name] = c
	}
	return out
}

func TestConcurrentRefreshRotationDoesNotLogOut(t *testing.T) {
	s, u := newSvc(t)
	w := httptest.NewRecorder()
	if err := s.StartSession(w, u.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	refresh := cookiesOf(w)[CookieRefresh]

	const n = 12
	var wg sync.WaitGroup
	results := make([]error, n)
	rotated := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/", nil)
			req.AddCookie(&http.Cookie{Name: CookieRefresh, Value: refresh.Value})
			rec := httptest.NewRecorder()
			_, results[i] = s.Authenticate(rec, req)
			_, rotated[i] = cookiesOf(rec)[CookieRefresh]
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
	w := httptest.NewRecorder()
	if err := s.StartSession(w, u.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	refresh := cookiesOf(w)[CookieRefresh]
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: CookieRefresh, Value: refresh.Value})
	if _, err := s.Authenticate(httptest.NewRecorder(), req); err != nil {
		t.Fatalf("first use: %v", err)
	}
	old := store.Ts(time.Now().Add(-2 * RotationGrace))
	if _, err := s.Store.DB.Exec(`UPDATE refresh_tokens SET superseded_at = ? WHERE superseded_at IS NOT NULL`, old); err != nil {
		t.Fatalf("age token: %v", err)
	}
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.AddCookie(&http.Cookie{Name: CookieRefresh, Value: refresh.Value})
	if _, err := s.Authenticate(httptest.NewRecorder(), req2); err != ErrBadToken {
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
