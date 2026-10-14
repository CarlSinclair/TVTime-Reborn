package store

import (
	"errors"
	"path/filepath"
	"testing"

	"tvt/db"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return New(d)
}

func TestSpoilerRule(t *testing.T) {
	cases := []struct {
		name         string
		author       bool
		spoil, notSp int
		want         bool
	}{
		{"clean", false, 0, 0, false},
		{"one report is enough", false, 1, 0, true},
		{"tie stays spoiler", false, 1, 1, true},
		{"flooded with not-spoiler clears", false, 1, 2, false},
		{"author tag always stands", true, 0, 5, true},
		{"only not-spoiler reports", false, 0, 3, false},
	}
	for _, c := range cases {
		cm := Comment{AuthorSpoiler: c.author, SpoilerReports: c.spoil, NotSpoilerReports: c.notSp}
		if got := cm.IsSpoiler(); got != c.want {
			t.Errorf("%s: IsSpoiler=%v want %v", c.name, got, c.want)
		}
	}
}

func seedEpisode(t *testing.T, s *Store) (int64, []int64) {
	t.Helper()
	sh := &Show{TMDBID: 100, Name: "Test Show", Status: "Returning Series"}
	id, err := s.SaveShow(sh, []SeasonInput{{Number: 1, Name: "S1", Episodes: []EpisodeInput{
		{TMDBID: 1, SeasonNumber: 1, EpisodeNumber: 1, Name: "Pilot", AirDate: "2020-01-01"},
		{TMDBID: 2, SeasonNumber: 1, EpisodeNumber: 2, Name: "Two", AirDate: "2020-01-08"},
		{TMDBID: 3, SeasonNumber: 1, EpisodeNumber: 3, Name: "Future", AirDate: "2999-01-01"},
	}}})
	if err != nil {
		t.Fatalf("save show: %v", err)
	}
	eps, err := s.Episodes(id, 1, 0)
	if err != nil || len(eps) != 3 {
		t.Fatalf("episodes: %v len=%d", err, len(eps))
	}
	return id, []int64{eps[0].ID, eps[1].ID, eps[2].ID}
}

func mkUser(t *testing.T, s *Store, name string) *User {
	t.Helper()
	u, err := s.CreateUser(name+"@example.com", name, "x", "en", false)
	if err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

func TestCommentsReportsAndBlocks(t *testing.T) {
	s := newTestStore(t)
	_, eps := seedEpisode(t, s)
	alice, bob, carol := mkUser(t, s, "alice"), mkUser(t, s, "bob"), mkUser(t, s, "carol")

	cid, err := s.AddComment(alice.ID, EpisodeTarget(eps[0]), 0, "hello", false, "")
	if err != nil {
		t.Fatalf("add comment: %v", err)
	}
	if _, err := s.AddComment(bob.ID, EpisodeTarget(eps[0]), cid, "reply", false, ""); err != nil {
		t.Fatalf("add reply: %v", err)
	}

	if err := s.ReportComment(alice.ID, cid, "spoiler", ""); !errors.Is(err, ErrSelfReport) {
		t.Fatalf("self report: got %v want ErrSelfReport", err)
	}
	if err := s.ReportComment(bob.ID, cid, "spoiler", ""); err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := s.ReportComment(bob.ID, cid, "spoiler", ""); !errors.Is(err, ErrAlreadyReported) {
		t.Fatalf("duplicate report: got %v want ErrAlreadyReported", err)
	}
	list, _, err := s.Comments(EpisodeTarget(eps[0]), carol.ID, 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || !list[0].IsSpoiler() {
		t.Fatalf("expected 2 comments with first flagged spoiler, got %d spoiler=%v", len(list), list[0].IsSpoiler())
	}
	if err := s.ReportComment(carol.ID, cid, "not_spoiler", ""); err != nil {
		t.Fatalf("not_spoiler report: %v", err)
	}
	if err := s.ReportComment(bob.ID, cid, "not_spoiler", ""); err != nil {
		t.Fatalf("second not_spoiler report: %v", err)
	}
	list, _, _ = s.Comments(EpisodeTarget(eps[0]), carol.ID, 50, 0)
	if list[0].IsSpoiler() {
		t.Fatalf("two not_spoiler reports vs one spoiler should clear the flag")
	}

	if err := s.Block(carol.ID, "alice"); err != nil {
		t.Fatalf("block: %v", err)
	}
	list, _, _ = s.Comments(EpisodeTarget(eps[0]), carol.ID, 50, 0)
	for _, c := range list {
		if c.UserID == alice.ID {
			t.Fatalf("carol still sees blocked alice")
		}
	}
	list, _, _ = s.Comments(EpisodeTarget(eps[0]), alice.ID, 50, 0)
	for _, c := range list {
		if c.UserID == carol.ID {
			t.Fatalf("alice sees carol who blocked her")
		}
	}
	if err := s.Block(carol.ID, "carol"); err == nil {
		t.Fatalf("self-block should fail")
	}
}

func TestTrackingAndUpNext(t *testing.T) {
	s := newTestStore(t)
	showID, eps := seedEpisode(t, s)
	u := mkUser(t, s, "dana")
	if err := s.Follow(u.ID, showID, "watching"); err != nil {
		t.Fatalf("follow: %v", err)
	}
	next, err := s.UpNext(u.ID, 10)
	if err != nil || len(next) != 1 || next[0].Episode.ID != eps[0] || next[0].Remaining != 2 {
		t.Fatalf("up next: err=%v %+v", err, next)
	}
	if err := s.MarkWatched(u.ID, eps[0], ""); err != nil {
		t.Fatalf("mark watched: %v", err)
	}
	next, _ = s.UpNext(u.ID, 10)
	if len(next) != 1 || next[0].Episode.ID != eps[1] {
		t.Fatalf("up next after watch: %+v", next)
	}
	n, err := s.MarkSeason(u.ID, showID, 1, true)
	if err != nil || n != 1 {
		t.Fatalf("mark season: n=%d err=%v (future episode must not be marked)", n, err)
	}
	p, _ := s.Progress(u.ID, showID)
	if p.Watched != 2 || p.Aired != 2 {
		t.Fatalf("progress %+v want 2/2", p)
	}
	if next, _ = s.UpNext(u.ID, 10); len(next) != 0 {
		t.Fatalf("expected no up next when caught up, got %d", len(next))
	}
	if err := s.MarkWatched(u.ID, 99999, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown episode: got %v want ErrNotFound", err)
	}
}
