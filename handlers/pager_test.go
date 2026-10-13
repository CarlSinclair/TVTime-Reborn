package handlers

import (
	"net/http/httptest"
	"testing"
)

func TestPaginateClampsAndKeepsOtherParameters(t *testing.T) {
	r := httptest.NewRequest("GET", "/shows?status=paused&page=3", nil)
	off, pg := paginate(r, "page", 10, 25, "")
	if off != 20 || pg.Page != 3 || pg.Pages != 3 || pg.NextURL != "" || pg.PrevURL != "/shows?page=2&status=paused" {
		t.Fatalf("%d %+v", off, pg)
	}
	r = httptest.NewRequest("GET", "/shows?page=99", nil)
	if off, pg := paginate(r, "page", 10, 25, ""); off != 20 || pg.Page != 3 {
		t.Fatalf("a page past the end is clamped: %d %+v", off, pg)
	}
	r = httptest.NewRequest("GET", "/u/x?recent=2", nil)
	if _, pg := paginate(r, "recent", 8, 30, "recent"); pg.PrevURL != "/u/x#recent" || pg.NextURL != "/u/x?recent=3#recent" {
		t.Fatalf("page one has no parameter and links keep the anchor: %+v", pg)
	}
	r = httptest.NewRequest("GET", "/x", nil)
	if off, pg := paginate(r, "page", 10, 0, ""); off != 0 || pg.Pages != 1 {
		t.Fatalf("an empty list is one page: %d %+v", off, pg)
	}
}
