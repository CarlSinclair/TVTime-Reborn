// Package legalpages is the single implementation of the "static / legal
// pages" CRUD - the same shape CShop and CTime use. CBots is a single
// always-on control-plane process on one domain, so it carries only the
// platform side (operator-authored pages served on cbots.top); the store
// and its editor partial / JS are byte-identical to the other two apps.
package legalpages

import (
	"database/sql"
	"fmt"
	"html"
	"html/template"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SlugRe is the allowed public-slug shape: lowercase letters, digits and
// hyphens, 1-64 chars, not starting with a hyphen.
var SlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ErrSlugTaken / ErrBadSlug are returned by SavePage on a slug problem;
// nothing is written when one occurs.
type ErrSlugTaken struct{ Slug string }

func (e ErrSlugTaken) Error() string { return "slug already in use: " + e.Slug }

type ErrBadSlug struct{ Slug string }

func (e ErrBadSlug) Error() string { return "invalid slug: " + e.Slug }

// Variant is one language version of a page.
type Variant struct {
	Lang    string
	Slug    string
	Title   string
	Body    string
	Enabled bool
	Exists  bool // false = no row authored for this language yet
}

// Page groups the language variants that share a page_id.
type Page struct {
	ID          int
	Enabled     bool // page-level on/off
	ShowUpdated bool // show the "last updated" date in the public page footer
	UpdatedAt   string
	UpdatedBy   string
	Variants    []Variant // one slot per language passed to ListPages, in order
}

// VariantInput is the per-language form data for one SavePage call.
type VariantInput struct {
	Lang    string
	Slug    string
	Title   string
	Body    string
	Enabled bool
	Delete  bool
}

// Resolved is the public lookup result.
type Resolved struct {
	PageID      int
	Lang        string
	Title       string
	Body        string
	UpdatedAt   string
	ShowUpdated bool
	Alternates  []Alt // sibling language variants, for hreflang
}

// Alt is one hreflang alternate.
type Alt struct {
	Lang string
	Slug string
}

// Store is the CRUD surface over one *_pages table.
type Store struct {
	DB    *sql.DB
	Table string
	MySQL bool
}

func (s Store) tbl() string {
	if s.MySQL {
		return "`" + s.Table + "`"
	}
	return `"` + s.Table + `"`
}

// EnsureSchema creates the table if missing. Safe on every boot.
func (s Store) EnsureSchema() error {
	var ddl string
	if s.MySQL {
		ddl = `CREATE TABLE IF NOT EXISTS ` + s.tbl() + ` (
			page_id      INT NOT NULL,
			lang         VARCHAR(16) NOT NULL,
			slug         VARCHAR(191) NOT NULL DEFAULT '',
			title        TEXT NULL,
			body         LONGTEXT NULL,
			enabled      TINYINT(1) NOT NULL DEFAULT 1,
			page_enabled TINYINT(1) NOT NULL DEFAULT 1,
			show_updated TINYINT(1) NOT NULL DEFAULT 0,
			updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_by   VARCHAR(191) NOT NULL DEFAULT '',
			PRIMARY KEY (page_id, lang),
			KEY idx_` + s.Table + `_slug (slug)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`
	} else {
		ddl = `CREATE TABLE IF NOT EXISTS ` + s.tbl() + ` (
			page_id      INTEGER NOT NULL,
			lang         TEXT NOT NULL,
			slug         TEXT NOT NULL DEFAULT '',
			title        TEXT NOT NULL DEFAULT '',
			body         TEXT NOT NULL DEFAULT '',
			enabled      INTEGER NOT NULL DEFAULT 1,
			page_enabled INTEGER NOT NULL DEFAULT 1,
			show_updated INTEGER NOT NULL DEFAULT 0,
			updated_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_by   TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (page_id, lang)
		)`
	}
	if _, err := s.DB.Exec(ddl); err != nil {
		return err
	}
	if !s.MySQL {
		_, err := s.DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_` + s.Table + `_slug ON ` + s.tbl() + `(slug) WHERE slug != ''`)
		return err
	}
	return nil
}

func (s Store) selectCols() string {
	return `page_id, lang, slug, title, body, enabled, page_enabled, show_updated, updated_at, updated_by`
}

// ListPages returns every page, each with one Variant slot per lang in
// langs (blank+Exists=false where unauthored), page_id ascending.
func (s Store) ListPages(langs []string) ([]Page, error) {
	rows, err := s.DB.Query(`SELECT ` + s.selectCols() + ` FROM ` + s.tbl())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := map[int]*Page{}
	varByID := map[int]map[string]Variant{}
	for rows.Next() {
		var (
			id                      int
			lang, slug, title, body string
			en, pen, su             int
			updatedAt, updatedBy    string
		)
		if err := rows.Scan(&id, &lang, &slug, &title, &body, &en, &pen, &su, &updatedAt, &updatedBy); err != nil {
			return nil, err
		}
		if byID[id] == nil {
			byID[id] = &Page{ID: id, Enabled: pen != 0, ShowUpdated: su != 0, UpdatedAt: updatedAt, UpdatedBy: updatedBy}
			varByID[id] = map[string]Variant{}
		}
		if updatedAt > byID[id].UpdatedAt {
			byID[id].UpdatedAt = updatedAt
			byID[id].UpdatedBy = updatedBy
		}
		if pen == 0 {
			byID[id].Enabled = false
		}
		if su != 0 {
			byID[id].ShowUpdated = true
		}
		varByID[id][lang] = Variant{Lang: lang, Slug: slug, Title: title, Body: body, Enabled: en != 0, Exists: true}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	ids := make([]int, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	out := make([]Page, 0, len(ids))
	for _, id := range ids {
		p := *byID[id]
		for _, l := range langs {
			if v, ok := varByID[id][l]; ok {
				p.Variants = append(p.Variants, v)
			} else {
				p.Variants = append(p.Variants, Variant{Lang: l})
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// Resolve is the public lookup: the enabled variant, on an enabled page,
// whose slug matches exactly. Alternates carries the page's other enabled
// language variants for hreflang.
func (s Store) Resolve(slug string) (Resolved, bool) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return Resolved{}, false
	}
	var r Resolved
	var su int
	err := s.DB.QueryRow(
		`SELECT page_id, lang, title, body, updated_at, show_updated FROM `+s.tbl()+
			` WHERE slug = ? AND enabled = 1 AND page_enabled = 1`, slug,
	).Scan(&r.PageID, &r.Lang, &r.Title, &r.Body, &r.UpdatedAt, &su)
	if err != nil {
		return Resolved{}, false
	}
	r.ShowUpdated = su != 0

	arows, err := s.DB.Query(
		`SELECT lang, slug FROM `+s.tbl()+
			` WHERE page_id = ? AND enabled = 1 AND page_enabled = 1 AND slug != ''`, r.PageID,
	)
	if err == nil {
		defer arows.Close()
		for arows.Next() {
			var a Alt
			if arows.Scan(&a.Lang, &a.Slug) == nil {
				r.Alternates = append(r.Alternates, a)
			}
		}
	}
	return r, true
}

// LiveSlugs lists every public slug that resolves, for the sitemap.
func (s Store) LiveSlugs() ([]string, error) {
	rows, err := s.DB.Query(`SELECT slug FROM ` + s.tbl() + ` WHERE slug != '' AND enabled = 1 AND page_enabled = 1 ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sl string
		if err := rows.Scan(&sl); err != nil {
			return nil, err
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

// SlugInUse reports whether slug already belongs to a page other than
// exceptPageID (pass 0 when adding).
func (s Store) SlugInUse(slug string, exceptPageID int) (bool, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return false, nil
	}
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM `+s.tbl()+` WHERE slug = ? AND page_id != ?`, slug, exceptPageID).Scan(&n)
	return n > 0, err
}

func (s Store) nextPageID() (int, error) {
	var n sql.NullInt64
	if err := s.DB.QueryRow(`SELECT MAX(page_id) FROM ` + s.tbl()).Scan(&n); err != nil {
		return 0, err
	}
	return int(n.Int64) + 1, nil
}

// AddPage creates a new page_id from add-form variants.
func (s Store) AddPage(vars []VariantInput, actor string) (int, error) {
	id, err := s.nextPageID()
	if err != nil {
		return 0, err
	}
	if err := s.SavePage(id, true, false, vars, actor); err != nil {
		return 0, err
	}
	return id, nil
}

// SavePage upserts every variant for one page. A variant with Delete set,
// or slug+title+body all empty, is removed. All slugs are validated and
// checked for cross-page uniqueness before anything is written.
func (s Store) SavePage(pageID int, pageEnabled, showUpdated bool, vars []VariantInput, actor string) error {
	for _, v := range vars {
		slug := strings.ToLower(strings.TrimSpace(v.Slug))
		if v.Delete || (slug == "" && strings.TrimSpace(v.Title) == "" && strings.TrimSpace(v.Body) == "") {
			continue
		}
		if slug != "" {
			if !SlugRe.MatchString(slug) {
				return ErrBadSlug{Slug: slug}
			}
			taken, err := s.SlugInUse(slug, pageID)
			if err != nil {
				return err
			}
			if taken {
				return ErrSlugTaken{Slug: slug}
			}
		}
	}

	pen, su := b2i(pageEnabled), b2i(showUpdated)
	for _, v := range vars {
		slug := strings.ToLower(strings.TrimSpace(v.Slug))
		empty := slug == "" && strings.TrimSpace(v.Title) == "" && strings.TrimSpace(v.Body) == ""
		if v.Delete || empty {
			if _, err := s.DB.Exec(`DELETE FROM `+s.tbl()+` WHERE page_id = ? AND lang = ?`, pageID, v.Lang); err != nil {
				return err
			}
			continue
		}
		var q string
		if s.MySQL {
			q = `INSERT INTO ` + s.tbl() + ` (page_id, lang, slug, title, body, enabled, page_enabled, show_updated, updated_at, updated_by)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?)
				ON DUPLICATE KEY UPDATE slug=VALUES(slug), title=VALUES(title), body=VALUES(body),
				  enabled=VALUES(enabled), page_enabled=VALUES(page_enabled), show_updated=VALUES(show_updated),
				  updated_at=CURRENT_TIMESTAMP, updated_by=VALUES(updated_by)`
		} else {
			q = `INSERT INTO ` + s.tbl() + ` (page_id, lang, slug, title, body, enabled, page_enabled, show_updated, updated_at, updated_by)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?)
				ON CONFLICT(page_id, lang) DO UPDATE SET slug=excluded.slug, title=excluded.title, body=excluded.body,
				  enabled=excluded.enabled, page_enabled=excluded.page_enabled, show_updated=excluded.show_updated,
				  updated_at=CURRENT_TIMESTAMP, updated_by=excluded.updated_by`
		}
		if _, err := s.DB.Exec(q, pageID, v.Lang, slug, strings.TrimSpace(v.Title), v.Body, b2i(v.Enabled), pen, su, actor); err != nil {
			return err
		}
	}
	if _, err := s.DB.Exec(`UPDATE `+s.tbl()+` SET page_enabled = ?, show_updated = ? WHERE page_id = ?`, pen, su, pageID); err != nil {
		return err
	}
	return nil
}

// DeletePage removes a whole page (all languages).
func (s Store) DeletePage(pageID int) error {
	_, err := s.DB.Exec(`DELETE FROM `+s.tbl()+` WHERE page_id = ?`, pageID)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

var (
	reMDLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reMDBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reMDItalic = regexp.MustCompile(`(^|[^*_])[*_]([^*_]+)[*_]`)
	reMDCode   = regexp.MustCompile("`([^`]+)`")
)

// SafeHTML renders operator Markdown to sanitised HTML. Every byte of input
// is HTML-escaped first, then a small whitelist of block and inline
// constructs is re-introduced - so raw HTML/script in the source can never
// survive, and only http(s)/mailto links are emitted. Dependency-free on
// purpose: CBots carries no Markdown library.
func SafeHTML(body string) template.HTML {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var out strings.Builder
	lines := strings.Split(body, "\n")
	inList, inOrdered, inPara := false, false, false

	closeList := func() {
		if inList {
			out.WriteString("</ul>\n")
			inList = false
		}
		if inOrdered {
			out.WriteString("</ol>\n")
			inOrdered = false
		}
	}
	closePara := func() {
		if inPara {
			out.WriteString("</p>\n")
			inPara = false
		}
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			closeList()
			closePara()
			continue
		}

		if h := headingLevel(trimmed); h > 0 {
			closeList()
			closePara()
			content := mdInline(strings.TrimSpace(trimmed[h+1:]))
			fmt.Fprintf(&out, "<h%d>%s</h%d>\n", h, content, h)
			continue
		}

		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			closePara()
			if inOrdered {
				out.WriteString("</ol>\n")
				inOrdered = false
			}
			if !inList {
				out.WriteString("<ul>\n")
				inList = true
			}
			fmt.Fprintf(&out, "<li>%s</li>\n", mdInline(strings.TrimSpace(trimmed[2:])))
			continue
		}

		if m := regexp.MustCompile(`^(\d+)\.\s+(.*)$`).FindStringSubmatch(trimmed); m != nil {
			closePara()
			if inList {
				out.WriteString("</ul>\n")
				inList = false
			}
			if !inOrdered {
				out.WriteString("<ol>\n")
				inOrdered = true
			}
			fmt.Fprintf(&out, "<li>%s</li>\n", mdInline(strings.TrimSpace(m[2])))
			continue
		}

		if strings.HasPrefix(trimmed, "> ") {
			closeList()
			closePara()
			fmt.Fprintf(&out, "<blockquote>%s</blockquote>\n", mdInline(strings.TrimSpace(trimmed[2:])))
			continue
		}

		closeList()
		if !inPara {
			out.WriteString("<p>")
			inPara = true
		} else {
			out.WriteString("<br>\n")
		}
		out.WriteString(mdInline(trimmed))
	}
	closeList()
	closePara()
	return template.HTML(out.String())
}

func headingLevel(s string) int {
	n := 0
	for n < len(s) && s[n] == '#' {
		n++
	}
	if n >= 1 && n <= 6 && n < len(s) && s[n] == ' ' {
		return n
	}
	return 0
}

// mdInline escapes text then re-applies inline formatting on the escaped
// string. Because the escape runs first, any literal "<" in the source is
// already "&lt;" and cannot become a tag.
func mdInline(s string) string {
	s = html.EscapeString(s)
	s = reMDCode.ReplaceAllString(s, "<code>$1</code>")
	s = reMDBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = reMDItalic.ReplaceAllString(s, "$1<em>$2</em>")
	s = reMDLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := reMDLink.FindStringSubmatch(m)
		text, href := sub[1], sub[2]
		low := strings.ToLower(strings.TrimSpace(href))
		if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") && !strings.HasPrefix(low, "mailto:") && !strings.HasPrefix(low, "/") {
			return text
		}
		return `<a href="` + href + `" rel="noopener nofollow">` + text + `</a>`
	})
	return s
}

// FormVariants reads the add-form's slug_<lang> / title_<lang> /
// body_<lang> / enabled_<lang> / del_<lang> fields.
func FormVariants(langs []string, get func(string) string) []VariantInput {
	return formVariants("", langs, get)
}

// FormVariantsFor reads an existing page's pid-prefixed fields:
// slug_<pid>_<lang> etc.
func FormVariantsFor(pid int, langs []string, get func(string) string) []VariantInput {
	return formVariants(strconv.Itoa(pid)+"_", langs, get)
}

func formVariants(prefix string, langs []string, get func(string) string) []VariantInput {
	out := make([]VariantInput, 0, len(langs))
	for _, l := range langs {
		out = append(out, VariantInput{
			Lang:    l,
			Slug:    strings.ToLower(strings.TrimSpace(get("slug_" + prefix + l))),
			Title:   strings.TrimSpace(get("title_" + prefix + l)),
			Body:    get("body_" + prefix + l),
			Enabled: get("enabled_"+prefix+l) == "1",
			Delete:  get("del_"+prefix+l) == "1",
		})
	}
	return out
}

// ParseIDs turns a "1,2,3" hidden field into ints.
func ParseIDs(csv string) []int {
	var out []int
	for _, p := range strings.Split(csv, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// FlashSlug maps a SavePage error to a short redirect query-param code.
func FlashSlug(err error) string {
	switch err.(type) {
	case ErrSlugTaken:
		return "slug_taken"
	case ErrBadSlug:
		return "bad_slug"
	default:
		return "save"
	}
}

// FlashError maps a SavePage error to a user-facing string.
func FlashError(err error) string {
	switch e := err.(type) {
	case ErrSlugTaken:
		return fmt.Sprintf("The slug %q is already used by another page.", e.Slug)
	case ErrBadSlug:
		return fmt.Sprintf("The slug %q must be lowercase letters, digits and hyphens.", e.Slug)
	default:
		return "Could not save the page."
	}
}
