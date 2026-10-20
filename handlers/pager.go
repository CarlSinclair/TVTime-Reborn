package handlers

import (
	"net/http"
	"net/url"
	"strconv"
)

// pager describes one page of a paginated list and the links to its neighbours.
type pager struct {
	Page, Pages, Total int
	PrevURL, NextURL   string
}

// paginate reads the page number from the query parameter param and returns the
// row offset for it, along with a pager whose links keep every other query
// parameter and end in anchor (without the #).
func paginate(r *http.Request, param string, per, total int, anchor string) (int, pager) {
	page, _ := strconv.Atoi(r.URL.Query().Get(param))
	pages := (total + per - 1) / per
	if pages < 1 {
		pages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	link := func(n int) string {
		q := url.Values{}
		for k, v := range r.URL.Query() {
			q[k] = v
		}
		if n <= 1 {
			q.Del(param)
		} else {
			q.Set(param, strconv.Itoa(n))
		}
		out := r.URL.Path
		if enc := q.Encode(); enc != "" {
			out += "?" + enc
		}
		if anchor != "" {
			out += "#" + anchor
		}
		return out
	}
	pg := pager{Page: page, Pages: pages, Total: total}
	if page > 1 {
		pg.PrevURL = link(page - 1)
	}
	if page < pages {
		pg.NextURL = link(page + 1)
	}
	return (page - 1) * per, pg
}
