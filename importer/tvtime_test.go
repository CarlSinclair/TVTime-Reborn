package importer

import (
	"archive/zip"
	"bytes"
	"errors"
	"testing"
)

func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const trackV2 = `user_id,key,created_at,s_id,series_name,season_number,episode_number,is_followed,is_for_later,is_archived
1,watch-episode-a-b,2022-05-01 23:52:32,100,Alpha,1,1,,,
1,watch-episode-a-c,2021-01-01 10:00:00,100,Alpha,1,1,,,
1,rewatch-episode-a-d-1,2023-01-01 10:00:00,100,Alpha,1,2,,,
1,watch-episode-a-e,2022-06-01 00:00:00,200,Beta,2,5,,,
1,user-series-x,2021-08-13 21:01:15,100,Alpha,,,true,false,true
1,user-series-y,2020-01-01 00:00:00,300,Gamma,,,false,true,false
1,watch-episode-bad,2022-06-01 00:00:00,,Nameless,1,1,,,
`

func TestParseTVTime(t *testing.T) {
	z := buildZip(t, map[string]string{
		"export/tracking-prod-records-v2.csv": trackV2,
		"export/user_show_special_status.csv": "updated_at,tv_show_name,user_id,tv_show_id,status,created_at\n2019-01-28 10:47:41,Alpha,1,100,favorite,2019-01-28 10:47:41\n",
		"export/ip_address.csv":               "ip\n203.0.113.9\n",
	})
	d, err := ParseTVTime(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Shows) != 3 {
		t.Fatalf("shows = %d, want 3: %+v", len(d.Shows), d.Shows)
	}
	var alpha, gamma Show
	for _, s := range d.Shows {
		switch s.TVDBID {
		case 100:
			alpha = s
		case 300:
			gamma = s
		}
	}
	if !alpha.Followed || !alpha.Archived || !alpha.Favorite || alpha.FollowedAt != "2021-08-13 21:01:15" {
		t.Fatalf("alpha = %+v", alpha)
	}
	if !gamma.ForLater || gamma.Followed {
		t.Fatalf("gamma = %+v", gamma)
	}
	if len(d.Watches) != 3 {
		t.Fatalf("watches = %d, want 3 (duplicate merged, rowless id dropped): %+v", len(d.Watches), d.Watches)
	}
	if w := d.Watches[0]; w.TVDBID != 100 || w.Season != 1 || w.Episode != 1 || w.At != "2021-01-01 10:00:00" {
		t.Fatalf("earliest watch should win: %+v", w)
	}
}

func TestParseTVTimeRejects(t *testing.T) {
	if _, err := ParseTVTime(bytes.NewReader([]byte("not a zip")), 9); !errors.Is(err, ErrNotExport) {
		t.Fatalf("garbage: %v", err)
	}
	z := buildZip(t, map[string]string{"readme.txt": "hello"})
	if _, err := ParseTVTime(bytes.NewReader(z), int64(len(z))); !errors.Is(err, ErrNotExport) {
		t.Fatalf("zip without the tracking file: %v", err)
	}
	z = buildZip(t, map[string]string{"tracking-prod-records-v2.csv": "user_id,key\n"})
	if _, err := ParseTVTime(bytes.NewReader(z), int64(len(z))); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty export: %v", err)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		s     Show
		watch bool
		want  string
		done  bool
	}{
		{Show{Followed: true}, false, "watching", false},
		{Show{Followed: true, Archived: true}, true, "paused", true},
		{Show{ForLater: true}, false, "plan", false},
		{Show{Favorite: true}, false, "paused", true},
		{Show{}, true, "dropped", true},
		{Show{}, false, "", false},
	}
	for _, c := range cases {
		got, done := statusFor(c.s, c.watch)
		if got != c.want || done != c.done {
			t.Errorf("statusFor(%+v,%v) = %q,%v want %q,%v", c.s, c.watch, got, done, c.want, c.done)
		}
	}
}
