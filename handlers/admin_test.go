package handlers_test

import (
	"strings"
	"testing"
)

func (e *env) makeAdmin(t *testing.T, name string) *client {
	c := e.signup(t, name, true)
	u := e.mustUser(t, name)
	if _, err := e.store.DB.Exec(`UPDATE users SET is_admin = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAdminTabsAndBadges(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)

	for _, p := range []string{"/admin", "/admin?tab=users", "/admin?tab=servers", "/admin?tab=reports"} {
		if code, body := boss.get(p); code != 200 || !strings.Contains(body, `aria-label="Admin"`) {
			t.Errorf("admin GET %s: %d", p, code)
		}
	}
	for _, p := range []string{"/admin/reports", "/admin/servers"} {
		if code, _ := boss.get(p); code != 301 {
			t.Errorf("old admin URL %s should redirect to its tab: %d", p, code)
		}
	}
	for _, p := range []string{"/admin", "/admin?tab=users"} {
		if code, _ := alice.get(p); code != 403 {
			t.Errorf("non-admin GET %s: %d", p, code)
		}
	}
	if code, _ := alice.post("/api/admin/users", map[string]any{"username": "x1x1x1", "password": "Passw0rd!x1"}); code != 403 {
		t.Errorf("non-admin create user: %d", code)
	}

	_, page := boss.get("/admin")
	if strings.Contains(page, `badge inline`) {
		t.Fatalf("no badges expected on a quiet server")
	}
	if err := e.store.PinPeer("friend.example", "a2V5"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.ReportUser(e.mustUser(t, "alice").ID, e.mustUser(t, "bobby").ID, "rude"); err != nil {
		t.Fatal(err)
	}
	_, page = boss.get("/admin?tab=users")
	if strings.Count(page, `class="badge inline">1<`) != 2 || strings.Count(page, `class="badge inline">2<`) != 1 {
		t.Fatalf("expected 1 on Servers, 1 on Reports and 2 on the Admin menu link")
	}
	_, servers := boss.get("/admin?tab=servers")
	if !strings.Contains(servers, "friend.example") || !strings.Contains(servers, `class="tag">New<`) {
		t.Fatalf("new server should be listed and tagged New")
	}
	_, page = boss.get("/admin?tab=users")
	if strings.Count(page, `class="badge inline">2<`) != 0 || strings.Count(page, `class="badge inline">1<`) != 2 {
		t.Fatalf("opening the Servers tab should clear its badge")
	}
	_ = bob
}

func TestAdminUserCrud(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	e.signup(t, "alice", true)

	if code, out := boss.post("/api/admin/users", map[string]any{"username": "newbie", "password": "Passw0rd!x1", "email": "newbie@example.com", "verified": true, "display": "New Bie"}); code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	u := e.mustUser(t, "newbie")
	if !u.EmailVerified || u.DisplayName != "New Bie" || u.IsAdmin {
		t.Fatalf("created user wrong: %+v", u)
	}
	if code, _ := boss.post("/api/admin/users", map[string]any{"username": "newbie", "password": "Passw0rd!x1"}); code != 409 {
		t.Fatalf("duplicate username: %d", code)
	}
	if code, _ := boss.post("/api/admin/users", map[string]any{"username": "weakpw", "password": "123"}); code != 400 {
		t.Fatalf("weak password: %d", code)
	}
	_, list := boss.get("/admin?tab=users&q=newb")
	if !strings.Contains(list, "@newbie") || strings.Contains(list, "@alice") {
		t.Fatalf("search should filter users")
	}

	path := func(id int64, verb string) string { return "/api/admin/users/" + itoa64(id) + "/" + verb }
	if code, out := boss.post(path(u.ID, "update"), map[string]any{"display": "Renamed", "email": "other@example.com", "verified": false}); code != 200 {
		t.Fatalf("update: %d %v", code, out)
	}
	u = e.mustUser(t, "newbie")
	if u.DisplayName != "Renamed" || u.Email != "other@example.com" || u.EmailVerified {
		t.Fatalf("update not applied: %+v", u)
	}
	if code, _ := boss.post(path(u.ID, "update"), map[string]any{"email": "alice@example.com"}); code != 409 {
		t.Fatalf("email taken: %d", code)
	}
	if code, _ := boss.post(path(u.ID, "ban"), map[string]any{"banned": true}); code != 200 {
		t.Fatalf("ban: %d", code)
	}
	if code, _ := boss.post(path(u.ID, "ban"), map[string]any{"banned": false}); code != 200 {
		t.Fatalf("unban: %d", code)
	}

	bossU := e.mustUser(t, "boss")
	if code, _ := boss.post(path(bossU.ID, "delete"), nil); code != 400 {
		t.Fatalf("self delete: %d", code)
	}
	if code, out := boss.post(path(u.ID, "delete"), nil); code != 200 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if _, err := e.store.UserByUsername("newbie"); err == nil {
		t.Fatalf("user should be gone")
	}
	if code, _ := boss.post("/api/admin/users/9999/delete", nil); code != 404 {
		t.Fatalf("delete missing: %d", code)
	}
}

func TestAdminSignupsToggleAndServerAdd(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	anon := newClient(t, e.srv.URL)
	if code, _ := boss.post("/api/admin/signups", map[string]any{"closed": true}); code != 200 {
		t.Fatalf("close signups: %d", code)
	}
	if code, out := anon.post("/api/auth/register", map[string]string{"email": "late@example.com", "username": "late", "password": "Passw0rd!x1"}); code != 403 {
		t.Fatalf("register while closed: %d %v", code, out)
	}
	if code, _ := boss.post("/api/admin/signups", map[string]any{"closed": false}); code != 200 {
		t.Fatalf("open signups: %d", code)
	}
	if code, out := anon.post("/api/auth/register", map[string]string{"email": "late@example.com", "username": "late", "password": "Passw0rd!x1"}); code != 200 {
		t.Fatalf("register while open: %d %v", code, out)
	}
	if code, _ := boss.post("/api/admin/servers/add", map[string]any{"host": "not a host"}); code != 400 {
		t.Fatalf("invalid host: %d", code)
	}
}
