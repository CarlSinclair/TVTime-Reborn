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
	if code, _ := boss.post(path(u.ID, "deactivate"), map[string]any{"deactivated": true}); code != 200 {
		t.Fatalf("deactivate: %d", code)
	}
	if code, _ := boss.post(path(u.ID, "deactivate"), map[string]any{"deactivated": false}); code != 200 {
		t.Fatalf("reactivate: %d", code)
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

func TestAdminOverviewListsRecentMail(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	if err := e.store.LogMail("someone@example.com", "Hello there", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.store.LogMail("other@example.com", "Broken one", errTest("smtp refused")); err != nil {
		t.Fatal(err)
	}
	_, page := boss.get("/admin")
	if !strings.Contains(page, "s***@example.com") || strings.Contains(page, "someone@example.com") {
		t.Fatalf("recipients must be masked")
	}
	if !strings.Contains(page, "Hello there") || !strings.Contains(page, "smtp refused") || !strings.Contains(page, `class="tag">Failed<`) {
		t.Fatalf("mail log should show subjects and failures")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestAdminConfigSavesOverridesAndSealsSecrets(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	alice := e.signup(t, "alice", true)
	if code, _ := alice.post("/api/admin/config", map[string]any{"group": "mail", "smtp_host": "x"}); code != 403 {
		t.Fatalf("non-admin config save: %d", code)
	}
	if code, _ := boss.post("/api/admin/config", map[string]any{"group": "nope"}); code != 400 {
		t.Fatalf("unknown group: %d", code)
	}
	if code, out := boss.post("/api/admin/config", map[string]any{"group": "mail", "smtp_host": "smtp.example.org", "smtp_port": "2525", "smtp_pass": "hunter2-secret", "mail_from": "hi@example.org"}); code != 200 {
		t.Fatalf("save mail: %d %v", code, out)
	}
	if raw := e.store.KVGet("cfg.smtp_pass"); raw == "" || strings.Contains(raw, "hunter2") {
		t.Fatalf("the SMTP password must be stored sealed, got %q", raw)
	}
	if e.store.KVGet("cfg.smtp_host") != "smtp.example.org" {
		t.Fatalf("plain settings are stored as typed")
	}
	_, page := boss.get("/admin?tab=config")
	if !strings.Contains(page, `value="hunter2-secret"`) || !strings.Contains(page, `value="smtp.example.org"`) || !strings.Contains(page, "Saved settings differ") {
		t.Fatalf("config tab shows the current values in the fields (the secret masked by its password input) and warns that a restart is pending")
	}
	if strings.Contains(page, "Set in the environment") || strings.Contains(page, `placeholder="smtp.example.org"`) {
		t.Fatalf("current values are values, not placeholders")
	}
	if code, out := boss.post("/api/admin/config", map[string]any{"group": "mail", "smtp_host": "", "smtp_port": "", "smtp_pass": ""}); code != 200 {
		t.Fatalf("clear: %d %v", code, out)
	}
	if e.store.KVGet("cfg.smtp_host") != "" || e.store.KVGet("cfg.smtp_pass") != "" {
		t.Fatalf("blank values and the clear box remove the overrides")
	}
	if code, out := boss.post("/api/admin/config", map[string]any{"group": "storage", "s3_endpoint": "https://a.r2.cloudflarestorage.com", "s3_bucket": "b"}); code != 400 {
		t.Fatalf("a partly filled storage group must be refused: %d %v", code, out)
	}
	if code, out := boss.post("/api/admin/config", map[string]any{"group": "federation", "federation": false}); code != 200 {
		t.Fatalf("federation toggle: %d %v", code, out)
	}
	_, servers := boss.get("/admin?tab=servers")
	if !strings.Contains(servers, `id="federation-switch"`) || strings.Contains(servers, `id="federation-switch" checked`) {
		t.Fatalf("the Servers tab shows federation as a switch, now off")
	}
	if code, out := boss.post("/api/admin/config/test", map[string]any{"group": "telegram"}); code != 400 {
		t.Fatalf("telegram test without settings: %d %v", code, out)
	}
	if code, out := boss.post("/api/admin/config/test", map[string]any{"group": "mail", "to": "boss@example.com"}); code != 200 {
		t.Fatalf("mail test (dev log mode): %d %v", code, out)
	}
	_, ov := boss.get("/admin")
	if !strings.Contains(ov, "boss@example.com") && !strings.Contains(ov, "b***@example.com") {
		t.Fatalf("the test email should appear in the mail log")
	}
}

func TestBanIsReadOnlyAndDeactivateBlocksSignIn(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	alice := e.signup(t, "alice", true)
	a := e.mustUser(t, "alice")
	if code, out := alice.post("/api/show/555/follow", map[string]string{"status": "watching"}); code != 200 {
		t.Fatalf("follow: %d %v", code, out)
	}
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	commentPath := "/api/episode/" + itoa64(eps[0].ID) + "/comments"
	if code, out := alice.post(commentPath, map[string]any{"body": "first"}); code != 200 {
		t.Fatalf("comment before ban: %d %v", code, out)
	}
	path := func(verb string) string { return "/api/admin/users/" + itoa64(a.ID) + "/" + verb }
	if code, _ := boss.post(path("ban"), map[string]any{"banned": true}); code != 200 {
		t.Fatalf("ban: %d", code)
	}
	if code, out := alice.post(commentPath, map[string]any{"body": "second"}); code != 403 || out["key"] != "err.read_only" {
		t.Fatalf("a banned account cannot comment: %d %v", code, out)
	}
	if code, _ := alice.get("/shows"); code != 200 {
		t.Fatalf("a banned account can still read: %d", code)
	}
	_, ep := alice.get("/episode/" + itoa64(eps[0].ID))
	if strings.Contains(ep, `id="comment-form"`) {
		t.Fatalf("a banned account gets no comment form")
	}
	if code, _ := boss.post(path("ban"), map[string]any{"banned": false}); code != 200 {
		t.Fatalf("unban: %d", code)
	}
	if code, out := alice.post(commentPath, map[string]any{"body": "third"}); code != 200 {
		t.Fatalf("comment after unban: %d %v", code, out)
	}

	if code, _ := boss.post(path("deactivate"), map[string]any{"deactivated": true}); code != 200 {
		t.Fatalf("deactivate: %d", code)
	}
	anon := newClient(t, e.srv.URL)
	if code, out := anon.post("/api/auth/login", map[string]string{"email": "alice@example.com", "password": "Passw0rd!x1"}); code != 403 {
		t.Fatalf("a deactivated account cannot sign in: %d %v", code, out)
	}
	if code, _ := boss.post(path("deactivate"), map[string]any{"deactivated": false}); code != 200 {
		t.Fatalf("reactivate: %d", code)
	}
	if code, out := anon.post("/api/auth/login", map[string]string{"email": "alice@example.com", "password": "Passw0rd!x1"}); code != 200 {
		t.Fatalf("a reactivated account can sign in: %d %v", code, out)
	}
	bossU := e.mustUser(t, "boss")
	for _, verb := range []string{"ban", "deactivate"} {
		if code, _ := boss.post("/api/admin/users/"+itoa64(bossU.ID)+"/"+verb, map[string]any{"banned": true, "deactivated": true}); code != 400 {
			t.Fatalf("an admin cannot %s themselves: %d", verb, code)
		}
	}
}

func TestAdminPasswordOverrideAndResetLink(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	e.signup(t, "alice", true)
	a := e.mustUser(t, "alice")
	path := func(verb string) string { return "/api/admin/users/" + itoa64(a.ID) + "/" + verb }
	if code, _ := boss.post(path("update"), map[string]any{"email": "alice@example.com", "password": "weak"}); code != 400 {
		t.Fatalf("a weak override password is refused: %d", code)
	}
	if code, out := boss.post(path("update"), map[string]any{"email": "alice@example.com", "password": "N3w-Passw0rd!z"}); code != 200 {
		t.Fatalf("override: %d %v", code, out)
	}
	anon := newClient(t, e.srv.URL)
	if code, _ := anon.post("/api/auth/login", map[string]string{"email": "alice@example.com", "password": "Passw0rd!x1"}); code != 401 {
		t.Fatalf("the old password stops working: %d", code)
	}
	if code, out := anon.post("/api/auth/login", map[string]string{"email": "alice@example.com", "password": "N3w-Passw0rd!z"}); code != 200 {
		t.Fatalf("the new password works: %d %v", code, out)
	}
	if code, out := boss.post(path("reset-password"), nil); code != 200 {
		t.Fatalf("reset link: %d %v", code, out)
	}
	if _, err := e.store.DB.Exec(`UPDATE users SET email = NULL WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := boss.post(path("reset-password"), nil); code != 400 {
		t.Fatalf("no email, no reset link: %d", code)
	}
}

func TestDeactivateSelfHidesCommentsAndSigningInRestoresThem(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)
	a := e.mustUser(t, "alice")
	if code, out := alice.post("/api/show/555/follow", map[string]string{"status": "watching"}); code != 200 {
		t.Fatalf("follow: %d %v", code, out)
	}
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	list := "/api/episode/" + itoa64(eps[0].ID) + "/comments"
	if code, out := alice.post(list, map[string]any{"body": "alice says hi"}); code != 200 {
		t.Fatalf("comment: %d %v", code, out)
	}
	if _, raw := bob.get(list); !strings.Contains(raw, "alice says hi") {
		t.Fatalf("bob should see alice's comment")
	}
	if code, out := alice.post("/api/account/deactivate", map[string]any{"password": "wrong"}); code != 401 {
		t.Fatalf("deactivation needs the password: %d %v", code, out)
	}
	if code, out := alice.post("/api/account/deactivate", map[string]any{"password": "Passw0rd!x1", "hide_comments": true}); code != 200 {
		t.Fatalf("deactivate: %d %v", code, out)
	}
	if _, raw := bob.get(list); strings.Contains(raw, "alice says hi") {
		t.Fatalf("a deactivated person's comments are hidden by default")
	}
	if code, _ := bob.get("/u/alice"); code != 404 {
		t.Fatalf("a deactivated profile is hidden: %d", code)
	}
	back := newClient(t, e.srv.URL)
	if code, out := back.post("/api/auth/login", map[string]string{"email": "alice@example.com", "password": "Passw0rd!x1"}); code != 200 {
		t.Fatalf("signing in reactivates: %d %v", code, out)
	}
	if u, _ := e.store.UserByID(a.ID); u.IsDeactivated {
		t.Fatalf("account should be active again")
	}
	if _, raw := bob.get(list); !strings.Contains(raw, "alice says hi") {
		t.Fatalf("hidden comments come back on reactivation")
	}
}

func TestDeactivateSelfCanDeleteCommentsInstead(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)
	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	list := "/api/episode/" + itoa64(eps[0].ID) + "/comments"
	alice.post(list, map[string]any{"body": "to be erased"})
	if code, out := alice.post("/api/account/deactivate", map[string]any{"password": "Passw0rd!x1", "delete_comments": true}); code != 200 {
		t.Fatalf("deactivate: %d %v", code, out)
	}
	back := newClient(t, e.srv.URL)
	back.post("/api/auth/login", map[string]string{"email": "alice@example.com", "password": "Passw0rd!x1"})
	if _, raw := bob.get(list); strings.Contains(raw, "to be erased") {
		t.Fatalf("deleted comments do not come back")
	}
	var n int
	e.store.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE body = 'to be erased'`).Scan(&n)
	if n != 0 {
		t.Fatalf("the text must be erased from the database, found %d", n)
	}
}

func TestAdminSuspensionIsNotLiftedBySigningInAndBanCommentChoices(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)
	a := e.mustUser(t, "alice")
	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	list := "/api/episode/" + itoa64(eps[0].ID) + "/comments"
	alice.post(list, map[string]any{"body": "borderline remark"})
	path := func(verb string) string { return "/api/admin/users/" + itoa64(a.ID) + "/" + verb }
	if code, _ := boss.post(path("ban"), map[string]any{"banned": true}); code != 200 {
		t.Fatalf("ban: %d", code)
	}
	if _, raw := bob.get(list); !strings.Contains(raw, "borderline remark") {
		t.Fatalf("a ban leaves comments visible unless the admin chooses otherwise")
	}
	if code, _ := boss.post(path("ban"), map[string]any{"banned": false}); code != 200 {
		t.Fatalf("unban: %d", code)
	}
	if code, _ := boss.post(path("ban"), map[string]any{"banned": true, "hide_comments": true}); code != 200 {
		t.Fatalf("ban and hide: %d", code)
	}
	if _, raw := bob.get(list); strings.Contains(raw, "borderline remark") {
		t.Fatalf("ban with hide hides the comments")
	}
	if code, _ := boss.post(path("ban"), map[string]any{"banned": false}); code != 200 {
		t.Fatalf("unban: %d", code)
	}
	if _, raw := bob.get(list); !strings.Contains(raw, "borderline remark") {
		t.Fatalf("unbanning restores hidden comments")
	}
	if code, _ := boss.post(path("deactivate"), map[string]any{"deactivated": true, "hide_comments": true}); code != 200 {
		t.Fatalf("suspend: %d", code)
	}
	anon := newClient(t, e.srv.URL)
	if code, _ := anon.post("/api/auth/login", map[string]string{"email": "alice@example.com", "password": "Passw0rd!x1"}); code != 403 {
		t.Fatalf("an admin's suspension is not lifted by signing in: %d", code)
	}
	if code, _ := boss.post(path("deactivate"), map[string]any{"deactivated": false}); code != 200 {
		t.Fatalf("reactivate: %d", code)
	}
}

func TestDeletingAnAccountNeverDeletesReplies(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)
	carl := e.signup(t, "carl", true)
	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	list := "/api/episode/" + itoa64(eps[0].ID) + "/comments"
	idOf := func(body string) int64 {
		var id int64
		e.store.DB.QueryRow(`SELECT id FROM comments WHERE body = ?`, body).Scan(&id)
		return id
	}
	alice.post(list, map[string]any{"body": "alice original"})
	carl.post("/api/show/555/follow", map[string]string{"status": "watching"})
	carl.post(list, map[string]any{"body": "carl original"})
	bob.post("/api/show/555/follow", map[string]string{"status": "watching"})
	bob.post(list, map[string]any{"body": "bobby to alice", "parent_id": idOf("alice original")})
	bob.post(list, map[string]any{"body": "bobby to carl", "parent_id": idOf("carl original")})

	if code, out := alice.post("/api/account/delete", map[string]any{"password": "Passw0rd!x1", "delete_comments": true}); code != 200 {
		t.Fatalf("delete with comments deleted: %d %v", code, out)
	}
	_, raw := bob.get(list)
	if !strings.Contains(raw, "bobby to alice") || strings.Contains(raw, "alice original") || !strings.Contains(raw, `"deleted":true`) {
		t.Fatalf("the reply stays under a Deleted placeholder: %s", raw)
	}
	if code, out := carl.post("/api/account/delete", map[string]any{"password": "Passw0rd!x1"}); code != 200 {
		t.Fatalf("delete keeping comments: %d %v", code, out)
	}
	_, raw = bob.get(list)
	if !strings.Contains(raw, "carl original") || !strings.Contains(raw, "bobby to carl") || !strings.Contains(raw, `"user":"deleted"`) {
		t.Fatalf("kept comments are anonymised under the deleted account and replies stay: %s", raw)
	}
	if _, err := e.store.UserByUsername("carl"); err == nil {
		t.Fatalf("the account itself is gone")
	}
	dan := e.signup(t, "dan", true)
	if code, _ := dan.post("/api/account/delete", map[string]any{"password": "Passw0rd!x1", "hide_comments": true, "delete_comments": true}); code != 400 {
		t.Fatalf("the two switches exclude each other: %d", code)
	}
}

func TestHiddenCommentsSayHiddenNotDeleted(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)
	alice.post("/api/show/555/follow", map[string]string{"status": "watching"})
	sh, _ := e.store.ShowByTMDB(555)
	eps, _ := e.store.Episodes(sh.ID, 1, 0)
	list := "/api/episode/" + itoa64(eps[0].ID) + "/comments"
	alice.post(list, map[string]any{"body": "alice thought"})
	var cid int64
	e.store.DB.QueryRow(`SELECT id FROM comments WHERE body = 'alice thought'`).Scan(&cid)
	bob.post("/api/show/555/follow", map[string]string{"status": "watching"})
	bob.post(list, map[string]any{"body": "bobby reply", "parent_id": cid})
	a := e.mustUser(t, "alice")
	boss.post("/api/admin/users/"+itoa64(a.ID)+"/ban", map[string]any{"banned": true, "hide_comments": true})
	_, raw := bob.get(list)
	if !strings.Contains(raw, `"hidden":true`) || strings.Contains(raw, `"deleted":true`) || strings.Contains(raw, "alice thought") || !strings.Contains(raw, "bobby reply") {
		t.Fatalf("a hidden comment is flagged hidden, not deleted, and its replies stay: %s", raw)
	}
}

func TestUsernameChangeRulesAndRedirect(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	alice := e.signup(t, "alice", true)
	bob := e.signup(t, "bobby", true)
	if code, _ := alice.post("/api/account/username", map[string]any{"username": "no"}); code != 400 {
		t.Fatalf("invalid name: %d", code)
	}
	if code, _ := alice.post("/api/account/username", map[string]any{"username": "bobby"}); code != 409 {
		t.Fatalf("taken name: %d", code)
	}
	if code, _ := alice.post("/api/account/username", map[string]any{"username": "Admin"}); code != 409 {
		t.Fatalf("reserved name: %d", code)
	}
	if code, out := alice.post("/api/account/username", map[string]any{"username": "alicia"}); code != 200 {
		t.Fatalf("rename: %d %v", code, out)
	}
	if code, _ := alice.post("/api/account/username", map[string]any{"username": "alicia2"}); code != 429 {
		t.Fatalf("a second change within 30 days is refused: %d", code)
	}
	resp, err := bob.http.Get(bob.base + "/u/alice")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 301 || resp.Header.Get("Location") != "/u/alicia" {
		t.Fatalf("the old name redirects: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, out := bob.post("/api/account/username", map[string]any{"username": "alice"}); code != 409 {
		t.Fatalf("a freed name is held for others: %d %v", code, out)
	}
	anon := newClient(t, e.srv.URL)
	if code, _ := anon.post("/api/auth/register", map[string]string{"email": "new@example.com", "username": "alice", "password": "Passw0rd!x1"}); code != 409 {
		t.Fatalf("a held name cannot be registered: %d", code)
	}
	if _, err := e.store.DB.Exec(`UPDATE username_history SET changed_at = '2000-01-01 00:00:00'`); err != nil {
		t.Fatal(err)
	}
	if code, _ := bob.get("/u/alice"); code != 404 {
		t.Fatalf("after 14 days the old name no longer redirects: %d", code)
	}
	a := e.mustUser(t, "alicia")
	if code, out := boss.post("/api/admin/users/"+itoa64(a.ID)+"/update", map[string]any{"username": "alicia_b", "email": "alice@example.com"}); code != 200 {
		t.Fatalf("admin rename ignores the 30 day limit: %d %v", code, out)
	}
	if code, _ := alice.post("/api/account/username", map[string]any{"username": "Alicia_B"}); code != 200 {
		t.Fatalf("a change of letter case is not a new name: %d", code)
	}
}

func TestServerShareSwitch(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	if err := e.store.PinPeer("friend.example", "a2V5"); err != nil {
		t.Fatal(err)
	}
	_, page := boss.get("/admin?tab=servers")
	if !strings.Contains(page, `data-server-approve="friend.example"`) || !strings.Contains(page, "Pending approval") {
		t.Fatalf("a server that connected by itself waits for approval")
	}
	if code, out := boss.post("/api/admin/servers/share", map[string]any{"host": "friend.example", "share": true}); code != 200 {
		t.Fatalf("share: %d %v", code, out)
	}
	if ok, _ := e.store.PeerShares("friend.example"); !ok {
		t.Fatalf("share not stored")
	}
	_, page = boss.get("/admin?tab=servers")
	if strings.Contains(page, `data-server-approve="friend.example"`) {
		t.Fatalf("an approved server has no approve button")
	}
	if code, _ := boss.post("/api/admin/servers/share", map[string]any{"host": "nobody.example", "share": true}); code != 404 {
		t.Fatalf("unknown server: %d", code)
	}
}

func TestUserEditModalAlwaysHasStatusControls(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	_, page := boss.get("/admin?tab=users")
	for _, want := range []string{`id="user-edit-status"`, `data-user-ban`, `data-user-deactivate`, `data-user-delete-modal`, `id="user-edit-protected"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the edit modal always has the ban, deactivate and delete controls, locked for protected accounts: missing %s", want)
		}
	}
	if !strings.Contains(page, `<div class="toolbar">`) || strings.Index(page, `for="signups-switch">Allow sign-ups`) > strings.Index(page, `id="signups-switch"`) {
		t.Fatalf("the plus button sits beside the search, and the switch comes after its label")
	}
}
