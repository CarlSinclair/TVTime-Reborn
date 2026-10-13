package handlers_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"testing"
	"time"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func (c *client) upload(path string, data []byte) (int, map[string]any) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "export.zip")
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", c.base+path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", c.csrf())
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("upload %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (c *client) importJob() map[string]any {
	_, body := c.get("/api/import/status")
	var out struct{ Job map[string]any }
	json.Unmarshal([]byte(body), &out)
	return out.Job
}

func (c *client) waitImport() map[string]any {
	for i := 0; i < 100; i++ {
		j := c.importJob()
		if j != nil && j["active"] == false {
			return j
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.t.Fatalf("import did not finish")
	return nil
}

const importTrack = `user_id,key,created_at,s_id,series_name,season_number,episode_number,is_followed,is_for_later,is_archived,episode_id
1,watch-episode-a,2020-02-01 10:00:00,5550,Fake Show,1,1,,,,70000
1,watch-episode-b,2020-02-02 10:00:00,5550,Fake Show,3,3,,,,70001
1,watch-episode-c,2020-02-03 10:00:00,5550,Fake Show,1,9,,,,70009
1,watch-episode-d,2020-02-04 10:00:00,9999,Nobody Knows,1,1,,,,70010
1,user-series-x,2020-01-01 00:00:00,5550,Fake Show,,,true,false,false,
1,user-series-y,2020-01-01 00:00:00,9999,Nobody Knows,,,true,false,false,
`

func TestTVTimeImportEndToEnd(t *testing.T) {
	e := newEnv(t)
	c := e.signup(t, "refugee", true)
	u, _ := e.store.UserByUsername("refugee")

	if code, _ := c.upload("/api/import/tvtime", []byte("not a zip")); code != 400 {
		t.Fatalf("garbage upload: %d", code)
	}
	if code, _ := c.upload("/api/import/tvtime", zipOf(t, map[string]string{"tracking-prod-records-v2.csv": "user_id,key\n"})); code != 400 {
		t.Fatalf("empty export upload: %d", code)
	}

	export := zipOf(t, map[string]string{
		"tracking-prod-records-v2.csv": importTrack,
		"user_show_special_status.csv": "updated_at,tv_show_name,user_id,tv_show_id,status,created_at\n2019-01-01 00:00:00,Fake Show,1,5550,favorite,2019-01-01 00:00:00\n",
	})
	if code, out := c.upload("/api/import/tvtime", export); code != 200 || out["ok"] != true {
		t.Fatalf("upload: %d %v", code, out)
	}
	j := c.waitImport()
	if j["status"] != "done" || j["matched"] != float64(1) || j["episodes_added"] != float64(2) || j["episodes_skipped"] != float64(2) || j["follows_added"] != float64(1) {
		t.Fatalf("job = %v", j)
	}
	un, _ := j["unmatched"].([]any)
	if len(un) != 1 || un[0].(map[string]any)["name"] != "Nobody Knows" {
		t.Fatalf("unmatched = %v", un)
	}

	sh, err := e.store.ShowByTMDB(555)
	if err != nil {
		t.Fatal(err)
	}
	f, err := e.store.FollowOf(u.ID, sh.ID)
	if err != nil || f == nil || f.Status != "watching" || !f.Favorite {
		t.Fatalf("follow = %+v %v", f, err)
	}
	p, _ := e.store.Progress(u.ID, sh.ID)
	if p.Watched != 2 {
		t.Fatalf("watched = %d, want 2", p.Watched)
	}
	var at string
	if err := e.store.DB.QueryRow(`SELECT MIN(watched_at) FROM watched WHERE user_id = ?`, u.ID).Scan(&at); err != nil || at != "2020-02-01 10:00:00" {
		t.Fatalf("original watch date lost: %q %v", at, err)
	}

	if code, _ := c.upload("/api/import/tvtime", export); code != 200 {
		t.Fatalf("second import: %d", code)
	}
	j = c.waitImport()
	if j["episodes_added"] != float64(0) || j["follows_added"] != float64(0) {
		t.Fatalf("second import must change nothing: %v", j)
	}
	notes, _ := e.store.Notifications(u.ID, 10, 0)
	if len(notes) == 0 {
		t.Fatalf("no completion notification")
	}
}

func TestImportRequiresLogin(t *testing.T) {
	e := newEnv(t)
	c := newClient(t, e.srv.URL)
	if code, _ := c.upload("/api/import/tvtime", []byte("x")); code != 401 {
		t.Fatalf("anonymous import: %d", code)
	}
	if code, _ := c.get("/api/import/status"); code != 401 {
		t.Fatalf("anonymous status: %d", code)
	}
}
