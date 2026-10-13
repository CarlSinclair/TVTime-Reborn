package handlers_test

import (
	"net/url"
	"testing"

	"tvt/auth"
)

func TestChangeEmailFlow(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "movey", true)
	e.signup(t, "taken", true)
	u, _ := e.store.UserByUsername("movey")
	const newMail = "new-address@example.com"

	if code, _ := c.post("/api/account/email/change", map[string]string{"email": newMail, "password": "wrong"}); code != 401 {
		t.Fatalf("wrong password must be refused: %d", code)
	}
	if code, out := c.post("/api/account/email/change", map[string]string{"email": "taken@example.com", "password": "Passw0rd!x1"}); code != 409 {
		t.Fatalf("an address in use must be refused: %d %v", code, out)
	}
	if code, _ := c.post("/api/account/email/change", map[string]string{"email": "movey@example.com", "password": "Passw0rd!x1"}); code != 400 {
		t.Fatalf("the same address must be refused: %d", code)
	}
	if code, out := c.post("/api/account/email/change", map[string]string{"email": newMail, "password": "Passw0rd!x1"}); code != 200 {
		t.Fatalf("change request: %d %v", code, out)
	}
	fresh, _ := e.store.UserByID(u.ID)
	if fresh.Email != "movey@example.com" || fresh.PendingEmail != newMail {
		t.Fatalf("nothing may change before the link is opened: %+v", fresh)
	}
	tok, err := e.auth.IssueEmailToken(u.ID, auth.PurposeEmail)
	if err != nil {
		t.Fatal(err)
	}
	if code, out := c.post("/api/auth/emailchange/consume", map[string]string{"token": tok}); code != 200 {
		t.Fatalf("consume: %d %v", code, out)
	}
	fresh, _ = e.store.UserByID(u.ID)
	if fresh.Email != newMail || !fresh.EmailVerified || fresh.PendingEmail != "" {
		t.Fatalf("after confirming: %+v", fresh)
	}
	if code, _ := c.post("/api/auth/emailchange/consume", map[string]string{"token": tok}); code != 400 {
		t.Fatalf("a link must work once: %d", code)
	}
	notes, _ := e.store.Notifications(u.ID, 20, 0)
	if len(notes) < 2 {
		t.Fatalf("the security notices were not stored: %d", len(notes))
	}
}

func TestWeakPasswordRejectedAtRegistration(t *testing.T) {
	e := newEnv(t)
	c := newClient(t, e.srv.URL)
	for _, p := range []string{"password123", "ALLUPPER1!", "NoDigits!!", "Sh0rt!"} {
		code, out := c.post("/api/auth/register", map[string]string{"email": "weak@example.com", "username": "weakling", "password": p})
		if code != 400 || out["key"] != "err.weak_password" {
			t.Errorf("%q accepted or wrong error: %d %v", p, code, out)
		}
	}
}

func TestChannelsCannotBeEnabledWithoutSetup(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "chan", false)
	for _, ch := range []string{"telegram", "email", "push"} {
		code, out := c.post("/api/notifications/settings", map[string]any{"master": map[string]bool{ch: true}, "prefs": map[string]any{}})
		if code != 400 {
			t.Errorf("%s enabled without being usable: %d %v", ch, code, out)
		}
	}
	if code, _ := c.post("/api/notifications/settings", map[string]any{"master": map[string]bool{"in_app": true}, "prefs": map[string]any{}}); code != 200 {
		t.Errorf("in-app must always be allowed: %d", code)
	}
}

func TestIdenticalNotificationsAreNotRepeated(t *testing.T) {
	e := newEnv(t)
	e.signup(t, "echo", true)
	u, _ := e.store.UserByUsername("echo")
	for i := 0; i < 5; i++ {
		e.nt.Send(u.ID, "email_changed", "/settings", map[string]any{"email": "a***@example.com"})
	}
	e.nt.Send(u.ID, "email_changed", "/settings", map[string]any{"email": "b***@example.com"})
	notes, _ := e.store.Notifications(u.ID, 20, 0)
	if len(notes) != 2 {
		t.Fatalf("notifications = %d, want 2 (five identical collapse to one, a different one still arrives)", len(notes))
	}
}

func TestOnlyOneCookieIsSet(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "cookie", true)
	if code, _ := c.post("/api/auth/login", map[string]string{"email": "cookie@example.com", "password": "Passw0rd!x1"}); code != 200 {
		t.Fatalf("login: %d", code)
	}
	c.get("/settings")
	u, _ := url.Parse(c.base)
	cookies := c.http.Jar.Cookies(u)
	if len(cookies) != 1 || cookies[0].Name != auth.CookieSession {
		t.Fatalf("cookies = %v, want the single %q cookie", cookies, auth.CookieSession)
	}
	if code, _ := c.get("/settings"); code != 200 {
		t.Fatalf("session did not survive: %d", code)
	}
}
