package handlers_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"tvt/media"
)

func pngBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func TestArtworkProxyCachesAndValidates(t *testing.T) {
	e := newEnv(t)
	var hits int32
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		switch r.URL.Path {
		case "/w342/goodposter1.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(pngBytes(40, 60))
		case "/w342/notanimage1.jpg":
			w.Write([]byte("<html>definitely not an image</html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cdn.Close()
	e.cfg.ImageBase = cdn.URL
	c := newClient(t, e.srv.URL)

	resp, err := http.Get(e.srv.URL + "/img/w342/goodposter1.jpg")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(body, pngBytes(40, 60)) || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("first fetch: %d cache=%q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	if resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("content type must come from the bytes, not the upstream header: %s", resp.Header.Get("Content-Type"))
	}
	before := atomic.LoadInt32(&hits)
	if status, _ := c.get("/img/w342/goodposter1.jpg"); status != 200 {
		t.Fatalf("cached fetch: %d", status)
	}
	if atomic.LoadInt32(&hits) != before {
		t.Fatalf("cached artwork went back to the upstream")
	}

	for _, bad := range []string{"/img/original/goodposter1.jpg", "/img/w342/ab.jpg", "/img/w342/goodposter1.gif", "/img/w1/goodposter1.jpg"} {
		if status, _ := c.get(bad); status != 404 {
			t.Errorf("%s should be rejected, got %d", bad, status)
		}
	}
	if status, _ := c.get("/img/w342/../etc.jpg"); status == 200 {
		t.Errorf("a traversal path must never be served")
	}
	if status, _ := c.get("/img/w342/missingposter.jpg"); status != 404 {
		t.Fatalf("upstream 404 should be a 404")
	}
	hitsAfterMiss := atomic.LoadInt32(&hits)
	c.get("/img/w342/missingposter.jpg")
	if atomic.LoadInt32(&hits) != hitsAfterMiss {
		t.Fatalf("a known-missing image was requested upstream again")
	}
	if status, _ := c.get("/img/w342/notanimage1.jpg"); status != 502 {
		t.Fatalf("a non-image upstream body must not be cached or served, got %d", status)
	}
}

func upload(t *testing.T, c *client, path string, data []byte) (int, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "x.png")
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", c.base+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", c.csrf())
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAvatarUploadStoresReencodedImageAndCleansUp(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "uma", true)
	code, body := upload(t, c, "/api/profile/avatar", pngBytes(300, 200))
	if code != 200 {
		t.Fatalf("upload: %d %s", code, body)
	}
	u := e.mustUser(t, "uma")
	if u.AvatarPath == "" || !strings.HasSuffix(u.AvatarPath, ".jpg") {
		t.Fatalf("avatar path %q", u.AvatarPath)
	}
	resp, err := http.Get(e.srv.URL + "/media/avatars/" + u.AvatarPath)
	if err != nil {
		t.Fatal(err)
	}
	served, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(served))
	if err != nil || format != "jpeg" || cfg.Width != 256 || cfg.Height != 256 {
		t.Fatalf("served avatar should be a 256x256 JPEG re-encode: %v %s %dx%d", err, format, cfg.Width, cfg.Height)
	}
	if code, _ := upload(t, c, "/api/profile/avatar", []byte("GIF89a not really an image at all, just text")); code != 400 {
		t.Fatalf("a non-image upload must be refused, got %d", code)
	}
	old := u.AvatarPath
	if code, _ := upload(t, c, "/api/profile/avatar", pngBytes(64, 64)); code != 200 {
		t.Fatalf("second upload failed")
	}
	if status, _ := c.get("/media/avatars/" + old); status != 404 {
		t.Fatalf("the replaced avatar should have been deleted from storage, got %d", status)
	}
	if code, out := c.post("/api/profile/avatar/remove", nil); code != 200 {
		t.Fatalf("remove: %d %v", code, out)
	}
	if e.mustUser(t, "uma").AvatarPath != "" {
		t.Fatalf("avatar not cleared")
	}
	if status, _ := c.get("/media/avatars/../../etc/passwd"); status == 200 {
		t.Fatalf("path traversal served a file")
	}
	if status, _ := c.get("/media/avatars/1-0123456789abcdef.jpg"); status != 404 {
		t.Fatalf("unknown media should 404, got %d", status)
	}
}

type publicStore struct {
	media.Store
	base string
}

func (p publicStore) PublicURL(key string) string { return p.base + "/" + key }

func TestPublicBucketModeRedirectsAndAllowsCDNImages(t *testing.T) {
	e := newEnv(t)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes(30, 45))
	}))
	defer cdn.Close()
	e.cfg.ImageBase = cdn.URL
	e.app.SetMedia(publicStore{Store: &media.Disk{Dir: t.TempDir()}, base: "https://cdn.example.net"})
	e.cfg.MediaPublicURL = "https://cdn.example.net"
	c := newClient(t, e.srv.URL)

	resp, err := c.http.Get(e.srv.URL + "/img/w342/redirectme1.jpg")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "https://cdn.example.net/tmdb/w342/redirectme1.jpg" {
		t.Fatalf("artwork should redirect to the bucket domain: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	su := e.signup(t, "xena", true)
	if code, body := upload(t, su, "/api/profile/avatar", pngBytes(64, 64)); code != 200 {
		t.Fatalf("upload: %d %s", code, body)
	}
	u := e.mustUser(t, "xena")
	_, page := su.get("/u/xena")
	if !strings.Contains(page, `src="https://cdn.example.net/avatars/`+u.AvatarPath+`"`) {
		t.Fatalf("avatar should point straight at the bucket domain")
	}
	r2, _ := http.Get(e.srv.URL + "/")
	r2.Body.Close()
	if csp := r2.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data: https://cdn.example.net;") {
		t.Fatalf("CSP must allow exactly the bucket domain for images: %s", csp)
	}
}
