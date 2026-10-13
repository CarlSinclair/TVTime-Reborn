package handlers_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"tvt/captcha"
)

func TestOperatorPagesPrivacyDefaultAndFooter(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	anon := newClient(t, e.srv.URL)

	_, priv := anon.get("/privacy")
	if !strings.Contains(priv, "What is stored") {
		t.Fatalf("the built-in privacy policy is the default")
	}
	_, foot := anon.get("/welcome")
	if !strings.Contains(foot, `href="/privacy"`) || !strings.Contains(foot, `href="/contact"`) || !strings.Contains(foot, `href="/about"`) {
		t.Fatalf("footer has privacy, about and contact")
	}
	if strings.Contains(foot, `href="/terms"`) {
		t.Fatalf("terms is optional and absent until added")
	}

	if code, out := boss.post("/api/admin/pages", map[string]any{"action": "add", "lang": "en", "slug": "terms", "title": "Terms of use", "enabled": true,
		"body": "# Rules\n\nBe **kind**.\n\n<script>alert(1)</script>"}); code != 200 {
		t.Fatalf("add terms: %d %v", code, out)
	}
	code, terms := anon.get("/terms")
	if code != 200 || !strings.Contains(terms, "<strong>kind</strong>") || strings.Contains(terms, "<script>alert") {
		t.Fatalf("the page renders its Markdown and strips raw HTML: %d", code)
	}
	if _, foot = anon.get("/welcome"); !strings.Contains(foot, `href="/terms">Terms of use`) {
		t.Fatalf("an enabled page appears in the footer")
	}
	if code, _ := boss.post("/api/admin/pages", map[string]any{"action": "add", "lang": "en", "slug": "terms", "title": "Again", "enabled": true, "body": "x"}); code != 409 {
		t.Fatalf("a slug can only belong to one page: %d", code)
	}
	if code, _ := boss.post("/api/admin/pages", map[string]any{"action": "add", "lang": "en", "slug": "Bad Slug!", "title": "x", "enabled": true, "body": "x"}); code != 400 {
		t.Fatalf("bad slug: %d", code)
	}

	if code, out := boss.post("/api/admin/pages", map[string]any{"action": "privacy_customize"}); code != 200 {
		t.Fatalf("customize: %d %v", code, out)
	}
	_, priv = anon.get("/privacy")
	if !strings.Contains(priv, "legal-body") || !strings.Contains(priv, "<h2>What is stored</h2>") {
		t.Fatalf("the customized policy starts from the built-in text, now as an editable page")
	}
	_, foot = anon.get("/welcome")
	if strings.Count(foot, `href="/privacy"`) != 1 {
		t.Fatalf("one privacy link in the footer, not two")
	}
	if code, _ := boss.post("/api/admin/pages", map[string]any{"action": "privacy_restore"}); code != 200 {
		t.Fatalf("restore: %d", code)
	}
	_, priv = anon.get("/privacy")
	if strings.Contains(priv, "legal-body") || !strings.Contains(priv, "What is stored") {
		t.Fatalf("back to the built-in policy")
	}
	var id int
	e.store.DB.QueryRow(`SELECT page_id FROM pages WHERE slug = 'terms'`).Scan(&id)
	boss.post("/api/admin/pages", map[string]any{"action": "save", "page_id": id, "page_enabled": false, "slug_en": "terms", "title_en": "Terms of use", "body_en": "x", "enabled_en": true})
	if code, _ := anon.get("/terms"); code == 200 {
		t.Fatalf("a disabled page is not public")
	}
	if code, _ := boss.post("/api/admin/pages", map[string]any{"action": "add_lang", "page_id": id, "lang": "pt", "slug": "termos", "title": "Termos", "body": "x", "enabled": true}); code != 200 {
		t.Fatalf("add a language to an existing page: %d", code)
	}
	var langs int
	e.store.DB.QueryRow(`SELECT COUNT(*) FROM pages WHERE page_id = ?`, id).Scan(&langs)
	if langs != 2 {
		t.Fatalf("a page can grow one language at a time: %d", langs)
	}
	boss.post("/api/admin/pages", map[string]any{"action": "save", "page_id": id, "page_enabled": true, "slug_en": "terms", "title_en": "Terms of use", "body_en": "x", "enabled_en": true})
	e.store.DB.QueryRow(`SELECT COUNT(*) FROM pages WHERE page_id = ?`, id).Scan(&langs)
	if langs != 2 {
		t.Fatalf("saving one language leaves the others alone: %d", langs)
	}
	if code, _ := boss.post("/api/admin/pages", map[string]any{"action": "delete", "page_id": id}); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	alice := e.signup(t, "alice", true)
	if code, _ := alice.post("/api/admin/pages", map[string]any{"action": "privacy_customize"}); code != 403 {
		t.Fatalf("only admins edit pages: %d", code)
	}
}

// solveCaptcha reads the challenge embedded in the page, does the proof of work
// and posts it, the way the script in the browser does.
func solveCaptcha(t *testing.T, c *client, page string) string {
	t.Helper()
	m := regexp.MustCompile(`name="captcha_id" value="([^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no captcha in the page")
	}
	id := m[1]
	parts := strings.Split(id, "_")
	if len(parts) != 4 || parts[3] != "0" {
		t.Fatalf("a first request needs only the proof of work: %q", id)
	}
	diff, _ := strconv.Atoi(parts[2])
	nonce := ""
	for n := 0; ; n++ {
		sum := sha256.Sum256([]byte(parts[1] + strconv.Itoa(n)))
		if strings.HasPrefix(hex.EncodeToString(sum[:]), strings.Repeat("0", diff)) {
			nonce = strconv.Itoa(n)
			break
		}
	}
	form := url.Values{"captcha_id": {id}, "captcha_solution": {"0"}, "pow_nonce": {nonce}, "committed": {"1"}}
	req, _ := http.NewRequest("POST", c.base+"/api/captcha/verify", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", c.csrf())
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ Success bool }
	json.NewDecoder(resp.Body).Decode(&out)
	if !out.Success {
		t.Fatalf("the captcha was not accepted")
	}
	return id
}

func TestContactPageSendsToTheOperator(t *testing.T) {
	captcha.ResetRisk()
	e := newEnv(t)
	anon := newClient(t, e.srv.URL)
	_, page := anon.get("/contact")
	if strings.Contains(page, `data-api="/api/contact"`) {
		t.Fatalf("no form until mail and a contact address are configured")
	}
	e.cfg.SMTPHost, e.cfg.MailFrom, e.cfg.ContactEmail = "smtp.test", "no-reply@test.example", "owner@test.example"
	_, page = anon.get("/contact")
	for _, want := range []string{`data-api="/api/contact"`, `href="mailto:owner@test.example"`, `https://t.me/tvt_test_bot?start=support`, `class="or-divider"`, `captcha-game-container`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the contact page needs %s", want)
		}
	}
	if strings.Index(page, "or-divider") > strings.Index(page, "<form") || strings.Index(page, "mailto:") > strings.Index(page, "or-divider") {
		t.Fatalf("the email and Telegram buttons come first, then OR, then the form")
	}
	body := map[string]any{"name": "Pat", "email": "pat@example.com", "message": "Hello operator"}
	if code, _ := anon.post("/api/contact", body); code != 400 {
		t.Fatalf("without a solved captcha nothing is sent: %d", code)
	}
	id := solveCaptcha(t, anon, page)
	body["captcha_id"] = id
	if code, out := anon.post("/api/contact", body); code != 200 {
		t.Fatalf("send: %d %v", code, out)
	}
	mails, _ := e.store.RecentMail(5)
	if len(mails) == 0 || !strings.HasPrefix(mails[0].To, "o***@") || !strings.Contains(mails[0].Subject, "contact: Pat") {
		t.Fatalf("the operator gets an email: %+v", mails)
	}
	if code, _ := anon.post("/api/contact", body); code != 400 {
		t.Fatalf("a solved captcha works once: %d", code)
	}
	before := len(mails)
	if code, _ := anon.post("/api/contact", map[string]any{"name": "Bot", "email": "b@example.com", "message": "spam", "website": "http://x"}); code != 200 {
		t.Fatalf("honeypot answers success")
	}
	if m, _ := e.store.RecentMail(5); len(m) != before {
		t.Fatalf("but sends nothing")
	}
}

func TestContactPageAddressFollowsTheVisitorsLanguage(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	anon := newClient(t, e.srv.URL)
	if code, _ := anon.get("/contact"); code != 200 {
		t.Fatalf("English visitors use /contact: %d", code)
	}
	for lang, slug := range map[string]string{"pt": "contato", "es": "contacto", "it": "contatti", "ru": "kontakty"} {
		c := newClient(t, e.srv.URL)
		fetch := func(path string) (*http.Response, string) {
			req, _ := http.NewRequest("GET", c.base+path, nil)
			req.Header.Set("Accept-Language", lang)
			resp, err := c.http.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			buf := make([]byte, 4096)
			for {
				n, err := resp.Body.Read(buf)
				b.Write(buf[:n])
				if err != nil {
					break
				}
			}
			resp.Body.Close()
			return resp, b.String()
		}
		resp, _ := fetch("/contact")
		if resp.StatusCode != 301 || resp.Header.Get("Location") != "/"+slug {
			t.Fatalf("%s: /contact should redirect to /%s, got %d %q", lang, slug, resp.StatusCode, resp.Header.Get("Location"))
		}
		resp, page := fetch("/" + slug)
		if resp.StatusCode != 200 || !strings.Contains(page, `href="/`+slug+`"`) {
			t.Fatalf("%s: /%s serves the page and the footer links to it: %d", lang, slug, resp.StatusCode)
		}
	}
	if code, _ := boss.post("/api/admin/pages", map[string]any{"action": "add", "lang": "en", "slug": "contato", "title": "x", "enabled": true, "body": "x"}); code != 409 {
		t.Fatalf("a page cannot take a contact address from any language: %d", code)
	}
}
