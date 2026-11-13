package store

import (
	"testing"
	"time"
)

const (
	chanA = "UCaaaaaaaaaaaaaaaaaaaaaa"
	chanB = "UCbbbbbbbbbbbbbbbbbbbbbb"
	listA = "PLHovnlOusNLhr7ttEReJUuC3QPHoohqZn"
)

func ytVideo(id, title, when string) YouTubeVideo {
	t, err := time.Parse(time.RFC3339, when)
	if err != nil {
		panic(err)
	}
	return YouTubeVideo{ID: id, Title: title, Published: t}
}

func TestRememberYouTubeSourceAllocatesLocalIDs(t *testing.T) {
	st := newTestStore(t)
	a, err := st.RememberYouTubeSource(YouTubeHit{Source: chanA, Name: "A"})
	if err != nil || a >= 0 {
		t.Fatalf("id=%d err=%v", a, err)
	}
	b, err := st.RememberYouTubeSource(YouTubeHit{Source: chanB, Name: "B"})
	if err != nil || b == a || b >= 0 {
		t.Fatalf("second id=%d first=%d err=%v", b, a, err)
	}
	again, err := st.RememberYouTubeSource(YouTubeHit{Source: chanA, Name: "A renamed"})
	if err != nil || again != a {
		t.Fatalf("a source must keep its id: %d %d %v", again, a, err)
	}
	h, err := st.YouTubeHitFor(a)
	if err != nil || h == nil || h.Source != chanA || h.Name != "A renamed" {
		t.Fatalf("hit = %+v err=%v", h, err)
	}
	adopted, err := st.RememberYouTubeSource(YouTubeHit{Source: chanA, Name: "A", ShowTMDB: -77})
	if err != nil || adopted != -77 {
		t.Fatalf("an adopted show id must win: %d %v", adopted, err)
	}
}

func TestApplyYouTubeVideosChannelNumbersByYear(t *testing.T) {
	st := newTestStore(t)
	showID, err := st.SaveYouTubeShow(&Show{TMDBID: -(youtubeIDBase + 1), Name: "Chan", Networks: "YouTube"}, chanA)
	if err != nil {
		t.Fatal(err)
	}
	again, err := st.SaveYouTubeShow(&Show{TMDBID: -5, Name: "Other"}, chanA)
	if err != nil || again != showID {
		t.Fatalf("a source must keep one show: %d %d %v", again, showID, err)
	}
	n, err := st.ApplyYouTubeVideos(showID, []YouTubeVideo{
		ytVideo("BBBBBBBBBBB", "Second", "2026-11-11T10:00:00Z"),
		ytVideo("AAAAAAAAAAA", "First", "2026-11-10T10:00:00Z"),
		ytVideo("CCCCCCCCCCC", "Last year", "2025-12-31T23:00:00Z"),
	}, true)
	if err != nil || n != 3 {
		t.Fatalf("added=%d err=%v", n, err)
	}
	n, err = st.ApplyYouTubeVideos(showID, []YouTubeVideo{ytVideo("AAAAAAAAAAA", "First", "2026-11-10T10:00:00Z")}, true)
	if err != nil || n != 0 {
		t.Fatalf("a known video must not be added twice: added=%d err=%v", n, err)
	}
	var season, ep int
	var airAt, still string
	if err := st.DB.QueryRow(`SELECT season_number, episode_number, air_at, still_path FROM episodes WHERE yt_video_id = 'BBBBBBBBBBB'`).Scan(&season, &ep, &airAt, &still); err != nil {
		t.Fatal(err)
	}
	if season != 2026 || ep != 2 || airAt != "2026-11-11 10:00:00" || still != "yt:BBBBBBBBBBB" {
		t.Fatalf("episode = s%d e%d at=%q still=%q", season, ep, airAt, still)
	}
	var total, seasons int
	if err := st.DB.QueryRow(`SELECT number_of_episodes, number_of_seasons FROM shows WHERE id = ?`, showID).Scan(&total, &seasons); err != nil || total != 3 || seasons != 2 {
		t.Fatalf("counts = %d %d %v", total, seasons, err)
	}
}

func TestApplyYouTubeVideosLegacyChannelIsStrict(t *testing.T) {
	st := newTestStore(t)
	id, err := st.SaveShow(&Show{TMDBID: -77, Name: "Old"}, []SeasonInput{{Number: 2026, Name: "Season 2026", Episodes: []EpisodeInput{
		{TMDBID: -1, SeasonNumber: 2026, EpisodeNumber: 5, Name: "Known Title!", AirDate: "2026-11-09"},
		{TMDBID: -2, SeasonNumber: 2026, EpisodeNumber: 6, Name: "Review", AirDate: "2026-11-08"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetYouTubeSource(id, chanB); err != nil {
		t.Fatal(err)
	}
	n, err := st.ApplyYouTubeVideos(id, []YouTubeVideo{
		ytVideo("DDDDDDDDDDD", "known title", "2026-11-09T15:30:00Z"),
		ytVideo("EEEEEEEEEEE", "Old unmatched", "2026-11-01T10:00:00Z"),
		ytVideo("FFFFFFFFFFF", "Brand new", "2026-11-12T08:00:00Z"),
		ytVideo("GGGGGGGGGGG", "RTX 5090 Review: the full story", "2026-11-12T09:00:00Z"),
	}, true)
	if err != nil || n != 2 {
		t.Fatalf("added=%d err=%v", n, err)
	}
	var ep int
	var airAt string
	if err := st.DB.QueryRow(`SELECT episode_number, air_at FROM episodes WHERE yt_video_id = 'DDDDDDDDDDD'`).Scan(&ep, &airAt); err != nil || ep != 5 || airAt != "2026-11-09 15:30:00" {
		t.Fatalf("legacy episode not attached: ep=%d at=%q err=%v", ep, airAt, err)
	}
	if err := st.DB.QueryRow(`SELECT episode_number FROM episodes WHERE yt_video_id = 'GGGGGGGGGGG'`).Scan(&ep); err != nil || ep != 8 {
		t.Fatalf("a short legacy title must not swallow other videos: ep=%d err=%v", ep, err)
	}
	var old int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM episodes WHERE yt_video_id = 'EEEEEEEEEEE'`).Scan(&old); err != nil || old != 0 {
		t.Fatalf("an old unmatched upload must be ignored: %d %v", old, err)
	}
}

func seedKnights(t *testing.T, st *Store) int64 {
	t.Helper()
	id, err := st.SaveShow(&Show{TMDBID: -258, Name: "Knights of Guinevere", Networks: "YouTube"}, []SeasonInput{{Number: 1, Name: "Season 1", Episodes: []EpisodeInput{
		{TMDBID: -1, SeasonNumber: 1, EpisodeNumber: 1, Name: "Pilot", AirDate: "2025-09-19"},
		{TMDBID: -2, SeasonNumber: 1, EpisodeNumber: 2, Name: "Wolves Around The Corner", AirDate: "2026-10-09"},
		{TMDBID: -3, SeasonNumber: 1, EpisodeNumber: 3, Name: "Episode 3", AirDate: "2027-01-22"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPlaylistAttachesToLegacySeries(t *testing.T) {
	st := newTestStore(t)
	id := seedKnights(t, st)
	titles := []string{"KNIGHTS OF GUINEVERE: PILOT", "KNIGHTS OF GUINEVERE: Ep 2 - Wolves Around The Corner"}
	tmdb, err := st.LegacyShowMatch("Knights of Guinevere", titles, true)
	if err != nil || tmdb != -258 {
		t.Fatalf("match = %d err=%v", tmdb, err)
	}
	if other, err := st.LegacyShowMatch("Knights of Guinevere", []string{"Totally unrelated"}, true); err != nil || other != 0 {
		t.Fatalf("an unrelated feed must not match: %d %v", other, err)
	}
	if other, err := st.LegacyShowMatch("Something else entirely", titles, true); err != nil || other != 0 {
		t.Fatalf("an unrelated name must not match: %d %v", other, err)
	}
	if err := st.SetYouTubeSource(id, listA); err != nil {
		t.Fatal(err)
	}
	n, err := st.ApplyYouTubeVideos(id, []YouTubeVideo{
		ytVideo("PPPPPPPPPPP", titles[0], "2025-09-19T22:00:06Z"),
		ytVideo("QQQQQQQQQQQ", titles[1], "2026-10-09T22:00:10Z"),
		ytVideo("RRRRRRRRRRR", "KNIGHTS OF GUINEVERE: Ep 3 - The Hunt", "2027-01-22T22:00:00Z"),
	}, false)
	if err != nil || n != 0 {
		t.Fatalf("every video belongs to a legacy episode: added=%d err=%v", n, err)
	}
	var name, vid string
	if err := st.DB.QueryRow(`SELECT name, yt_video_id FROM episodes WHERE show_id = ? AND season_number = 1 AND episode_number = 3`, id).Scan(&name, &vid); err != nil || vid != "RRRRRRRRRRR" || name != "KNIGHTS OF GUINEVERE: Ep 3 - The Hunt" {
		t.Fatalf("placeholder episode not filled: %q %q %v", name, vid, err)
	}
}

func TestResolveYouTubeSource(t *testing.T) {
	st := newTestStore(t)
	seedKnights(t, st)
	got, err := st.ResolveYouTubeSource(listA, -258, "Knights of Guinevere", "yt:PPPPPPPPPPP")
	if err != nil || got != -258 {
		t.Fatalf("a peer's show must meet the local one: %d %v", got, err)
	}
	if src, err := st.YouTubeSourceOf(-258); err != nil || src != listA {
		t.Fatalf("source = %q err=%v", src, err)
	}
	fresh, err := st.ResolveYouTubeSource(chanA, -999999, "Unknown", "")
	if err != nil || fresh >= -youtubeIDBase+1 {
		t.Fatalf("an unknown source needs a new local id: %d %v", fresh, err)
	}
	if again, err := st.ResolveYouTubeSource(chanA, 0, "Unknown", ""); err != nil || again != fresh {
		t.Fatalf("the same source must give the same id: %d %d %v", again, fresh, err)
	}
}

func TestBackfillYouTubePlaylistInfersDates(t *testing.T) {
	st := newTestStore(t)
	showID, err := st.SaveYouTubeShow(&Show{TMDBID: -(youtubeIDBase + 9), Name: "List", Networks: "YouTube"}, listA)
	if err != nil {
		t.Fatal(err)
	}
	recent := time.Now().UTC().AddDate(0, 0, -30)
	listed := []YouTubeVideo{
		{ID: "AAAAAAAAAAA", Title: "Old one"},
		{ID: "BBBBBBBBBBB", Title: "Old two"},
		{ID: "CCCCCCCCCCC", Title: "Dated", Published: recent},
	}
	n, err := st.BackfillYouTubePlaylist(showID, listed)
	if err != nil || n != 3 {
		t.Fatalf("added=%d err=%v", n, err)
	}
	var e1, e2, e3 string
	var s1, s3 string
	if err := st.DB.QueryRow(`SELECT air_date, air_source FROM episodes WHERE yt_video_id = 'AAAAAAAAAAA'`).Scan(&e1, &s1); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT air_date FROM episodes WHERE yt_video_id = 'BBBBBBBBBBB'`).Scan(&e2); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT air_date, air_source FROM episodes WHERE yt_video_id = 'CCCCCCCCCCC'`).Scan(&e3, &s3); err != nil {
		t.Fatal(err)
	}
	limit := time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02")
	if !(e1 < e2 && e2 <= limit) || s1 != "inferred" || s3 != "youtube" || e3 != recent.Format("2006-01-02") {
		t.Fatalf("dates = %s %s %s sources %s %s limit %s", e1, e2, e3, s1, s3, limit)
	}
	var ep int
	if err := st.DB.QueryRow(`SELECT episode_number FROM episodes WHERE yt_video_id = 'CCCCCCCCCCC'`).Scan(&ep); err != nil || ep != 3 {
		t.Fatalf("playlist position must be the episode number: %d %v", ep, err)
	}
	again, err := st.BackfillYouTubePlaylist(showID, listed)
	if err != nil || again != 0 {
		t.Fatalf("a show with episodes must not be backfilled again: %d %v", again, err)
	}
}

func TestBackfillYouTubeChannelRenumbersByTime(t *testing.T) {
	st := newTestStore(t)
	showID, err := st.SaveYouTubeShow(&Show{TMDBID: -(youtubeIDBase + 4), Name: "Chan", Networks: "YouTube"}, chanA)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := st.ApplyYouTubeVideos(showID, []YouTubeVideo{ytVideo("DDDDDDDDDDD", "Newest", "2026-06-01T10:00:00Z")}, true); err != nil || n != 1 {
		t.Fatalf("feed: %d %v", n, err)
	}
	n, err := st.BackfillYouTubeChannel(showID, []YouTubeVideo{
		ytVideo("AAAAAAAAAAA", "Oldest", "2026-01-01T10:00:00Z"),
		ytVideo("BBBBBBBBBBB", "Middle", "2026-03-01T10:00:00Z"),
		ytVideo("DDDDDDDDDDD", "Newest", "2026-06-01T10:00:00Z"),
		ytVideo("CCCCCCCCCCC", "Last year", "2025-12-31T10:00:00Z"),
	})
	if err != nil || n != 3 {
		t.Fatalf("added=%d err=%v", n, err)
	}
	want := map[string][2]int{"AAAAAAAAAAA": {2026, 1}, "BBBBBBBBBBB": {2026, 2}, "DDDDDDDDDDD": {2026, 3}, "CCCCCCCCCCC": {2025, 1}}
	for id, w := range want {
		var season, ep int
		if err := st.DB.QueryRow(`SELECT season_number, episode_number FROM episodes WHERE yt_video_id = ?`, id).Scan(&season, &ep); err != nil || season != w[0] || ep != w[1] {
			t.Fatalf("%s is s%d e%d (%v), want s%d e%d", id, season, ep, err, w[0], w[1])
		}
	}
}
