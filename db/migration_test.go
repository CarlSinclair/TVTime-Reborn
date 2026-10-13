package db

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestProfilePostsMigrationKeepsCommentsAndReports(t *testing.T) {
	d, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "m.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	apply := func(name string) {
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			t.Fatalf("apply %s: %v", name, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var last string
	for _, n := range names {
		if strings.HasPrefix(n, "007_") {
			last = n
			break
		}
		apply(n)
	}
	if last == "" {
		t.Fatal("007 migration missing")
	}
	mustExec := func(q string, args ...any) {
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO users (username, email) VALUES ('a','a@x.io'), ('b','b@x.io')`)
	mustExec(`INSERT INTO shows (tmdb_id, name) VALUES (1,'S')`)
	mustExec(`INSERT INTO episodes (show_id, tmdb_id, season_number, episode_number) VALUES (1,1,1,1)`)
	mustExec(`INSERT INTO comments (episode_id, user_id, body) VALUES (1,1,'hello')`)
	mustExec(`INSERT INTO comments (episode_id, user_id, parent_id, body) VALUES (1,2,1,'reply')`)
	mustExec(`INSERT INTO comment_reports (comment_id, user_id, kind, reason) VALUES (1,2,'abuse','rude')`)

	apply(last)

	var comments, replies, reports int
	if err := d.QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&comments); err != nil || comments != 2 {
		t.Fatalf("comments after migration = %d %v", comments, err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM comments WHERE parent_id = 1`).Scan(&replies); err != nil || replies != 1 {
		t.Fatalf("reply link lost: %d %v", replies, err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM comment_reports`).Scan(&reports); err != nil || reports != 1 {
		t.Fatalf("reports must survive the rebuild: %d %v", reports, err)
	}
	var schema string
	if err := d.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'comments'`).Scan(&schema); err != nil || strings.Contains(schema, "comments_new") {
		t.Fatalf("the parent link must point at the final table name: %q %v", schema, err)
	}
	mustExec(`INSERT INTO comments (profile_id, user_id, body) VALUES (1,1,'a post')`)
	if _, err := d.Exec(`INSERT INTO comments (user_id, body) VALUES (1,'neither')`); err == nil {
		t.Fatalf("a comment must belong to an episode or a profile")
	}
}
