package handlers_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func solidPNG(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.NRGBA{R: 200, G: 30, B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func (c *client) uploadFile(path string, data []byte) int {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "logo.png")
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", c.base+path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", c.csrf())
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestLogoReplacesTheSiteNameAndIsAdminEditable(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	alice := e.signup(t, "alice", true)
	_, page := alice.get("/")
	if !strings.Contains(page, `class="brand-logo" src="/static/img/icon.svg"`) || strings.Contains(page, `class="brand" href="/">TVT`) {
		t.Fatalf("the top-left link is the logo, with the default until one is uploaded")
	}
	if code := alice.uploadFile("/api/admin/logo", solidPNG(40, 40)); code != 403 {
		t.Fatalf("only admins may change the logo: %d", code)
	}
	if code := boss.uploadFile("/api/admin/logo", []byte("not an image")); code != 400 {
		t.Fatalf("junk is refused: %d", code)
	}
	if code := boss.uploadFile("/api/admin/logo", solidPNG(1600, 400)); code != 200 {
		t.Fatalf("upload: %d", code)
	}
	_, page = alice.get("/")
	m := regexp.MustCompile(`class="brand-logo" src="(/media/branding/logo-[0-9a-f]{16}\.png)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("the uploaded logo is in the top bar: %s", page[:600])
	}
	resp, err := http.Get(e.srv.URL + m[1])
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	img, err := png.Decode(resp.Body)
	if err != nil || resp.StatusCode != 200 || img.Bounds().Dx() > 640 || img.Bounds().Dy() > 160 || img.Bounds().Dx() != 4*img.Bounds().Dy() {
		t.Fatalf("the logo is scaled to fit and keeps its proportions: %v %v", err, img.Bounds())
	}
	if code, _ := boss.post("/api/admin/logo/remove", nil); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	_, page = alice.get("/")
	if !strings.Contains(page, `src="/static/img/icon.svg"`) {
		t.Fatalf("back to the default logo")
	}
}

func TestVisitorsGetAHomepageAndMembersGetTheApp(t *testing.T) {
	e := newEnv(t)
	anon := newClient(t, e.srv.URL)
	code, page := anon.get("/")
	if code != 200 || !strings.Contains(page, `href="/register"`) || !strings.Contains(page, "Never lose your place") {
		t.Fatalf("a visitor sees the public homepage, not a redirect to log in: %d", code)
	}
	if code, _ := anon.get("/shows"); code != 200 && code != 303 {
		t.Fatalf("the app itself stays behind login: %d", code)
	}
	alice := e.signup(t, "alice", true)
	_, home := alice.get("/")
	if strings.Contains(home, "Never lose your place") || !strings.Contains(home, "data-home-tab") {
		t.Fatalf("a signed-in member gets the app at the root")
	}
	if _, w := alice.get("/welcome"); !strings.Contains(w, "Never lose your place") || !strings.Contains(w, `href="/welcome"`) {
		t.Fatalf("members can still open the homepage, and the footer links to it")
	}
}
