package handlers_test

import (
	"encoding/json"
	"image/png"
	"net/http"
	"strings"
	"testing"
)

func TestManifestMeetsInstallRequirements(t *testing.T) {
	e := newEnv(t)
	anon := newClient(t, e.srv.URL)
	code, body := anon.get("/manifest.webmanifest")
	if code != 200 {
		t.Fatalf("manifest: %d", code)
	}
	var m struct {
		Name, Display string
		StartURLRaw   string `json:"start_url"`
		Icons         []struct{ Src, Sizes, Type, Purpose string }
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	if m.Name == "" || m.StartURLRaw == "" || m.Display != "standalone" {
		t.Fatalf("manifest basics missing: %+v", m)
	}
	want := map[string]bool{"192x192": false, "512x512": false}
	maskable := false
	for _, ic := range m.Icons {
		if ic.Type != "image/png" {
			continue
		}
		resp, err := http.Get(e.srv.URL + ic.Src)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("icon %s: %d %v", ic.Src, resp.StatusCode, err)
		}
		if b := img.Bounds(); b.Dx() != b.Dy() || !strings.HasPrefix(ic.Sizes, itoa64(int64(b.Dx()))+"x") {
			t.Fatalf("icon %s claims %s but is %v", ic.Src, ic.Sizes, b)
		}
		if ic.Purpose == "any" {
			if _, ok := want[ic.Sizes]; ok {
				want[ic.Sizes] = true
			}
		}
		if ic.Purpose == "maskable" {
			maskable = true
		}
	}
	for size, ok := range want {
		if !ok {
			t.Errorf("Chrome needs a %s PNG icon with purpose any", size)
		}
	}
	if !maskable {
		t.Errorf("a maskable icon is expected")
	}
}
