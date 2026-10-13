package importer

import (
	"context"
	"os"
	"sort"
	"testing"
	"tvt/tmdb"
)

func TestParseSampleExport(t *testing.T) {
	path := os.Getenv("TVT_TVTIME_SAMPLE")
	if path == "" {
		t.Skip("set TVT_TVTIME_SAMPLE to a real TV Time export zip to run this")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseTVTime(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	followed, later, archived, fav := 0, 0, 0, 0
	for _, s := range d.Shows {
		if s.Followed {
			followed++
		}
		if s.ForLater {
			later++
		}
		if s.Archived {
			archived++
		}
		if s.Favorite {
			fav++
		}
	}
	t.Logf("shows=%d followed=%d for_later=%d archived=%d favorite=%d watches=%d", len(d.Shows), followed, later, archived, fav, len(d.Watches))
}

func TestResolveSampleExport(t *testing.T) {
	path, key := os.Getenv("TVT_TVTIME_SAMPLE"), os.Getenv("TVT_TMDB_KEY")
	if path == "" || key == "" {
		t.Skip("set TVT_TVTIME_SAMPLE and TVT_TMDB_KEY to resolve a real export against TMDB (read-only)")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	d, err := ParseTVTime(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{TMDB: tmdb.New(key)}
	counts := map[string]int{}
	var misses []string
	for _, s := range d.Shows {
		id, reason := r.resolve(context.Background(), s)
		if id == 0 {
			counts[reason]++
			if len(misses) < 15 {
				misses = append(misses, s.Name+" ["+reason+"]")
			}
		} else {
			counts["ok"]++
		}
	}
	byShow := map[int64][]Watch{}
	for _, w := range d.Watches {
		byShow[w.TVDBID] = append(byShow[w.TVDBID], w)
	}
	shows := append([]Show(nil), d.Shows...)
	sort.Slice(shows, func(i, j int) bool { return len(byShow[shows[i].TVDBID]) > len(byShow[shows[j].TVDBID]) })
	checked := 0
	for _, s := range shows {
		if checked >= 12 || len(byShow[s.TVDBID]) < 10 {
			break
		}
		id, _ := r.resolve(context.Background(), s)
		if id == 0 {
			continue
		}
		det, err := r.TMDB.ShowDetails(context.Background(), id, "en-US")
		if err != nil {
			t.Logf("details %s: %v", s.Name, err)
			continue
		}
		var nums []int
		for _, se := range det.Seasons {
			nums = append(nums, se.SeasonNumber)
		}
		seasons, err := r.TMDB.Seasons(context.Background(), id, nums, "en-US")
		if err != nil {
			t.Logf("seasons %s: %v", s.Name, err)
			continue
		}
		have := map[[2]int]bool{}
		for _, sd := range seasons {
			for _, ep := range sd.Episodes {
				have[[2]int{ep.SeasonNumber, ep.EpisodeNumber}] = true
			}
		}
		hit := 0
		for _, w := range byShow[s.TVDBID] {
			if have[[2]int{w.Season, w.Episode}] {
				hit++
			}
		}
		t.Logf("episodes %-32.32s %4d of %4d matched", s.Name, hit, len(byShow[s.TVDBID]))
		checked++
	}
	t.Logf("resolved: %v", counts)
	t.Logf("sample misses: %v", misses)
}
