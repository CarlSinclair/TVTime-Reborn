package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"tvt/i18n"

	"tvt/legalpages"
	"tvt/logger"
)

// footerLink is an operator-written page that appears in the footer.
type footerLink struct {
	Slug, Title string
}

// pageCache remembers which public slugs exist and what the footer shows, so a
// request needs no query for either. It is rebuilt whenever pages change.
type pageCache struct {
	mu            sync.RWMutex
	slugs         map[string]bool
	footer        map[string][]footerLink
	customPrivacy bool
}

func (a *App) pages() legalpages.Store {
	return legalpages.Store{DB: a.Store.DB, Table: "pages"}
}

func (a *App) reloadPages() {
	langs := a.I18n.Langs()
	list, err := a.pages().ListPages(langs)
	if err != nil {
		logger.Error("PAGES reload: %v", err)
		return
	}
	slugs := map[string]bool{}
	footer := map[string][]footerLink{}
	custom := false
	for _, p := range list {
		if !p.Enabled {
			continue
		}
		byLang := map[string]legalpages.Variant{}
		for _, v := range p.Variants {
			if v.Exists && v.Enabled && v.Slug != "" {
				slugs[v.Slug] = true
				byLang[v.Lang] = v
				if v.Slug == "privacy" {
					custom = true
				}
			}
		}
		for _, l := range langs {
			v, ok := byLang[l]
			if !ok {
				if v, ok = byLang[i18n.Fallback]; !ok {
					continue
				}
			}
			title := v.Title
			if title == "" {
				title = v.Slug
			}
			footer[l] = append(footer[l], footerLink{Slug: v.Slug, Title: title})
		}
	}
	a.pageCache.mu.Lock()
	a.pageCache.slugs, a.pageCache.footer, a.pageCache.customPrivacy = slugs, footer, custom
	a.pageCache.mu.Unlock()
}

func (a *App) isPageSlug(p string) bool {
	a.pageCache.mu.RLock()
	defer a.pageCache.mu.RUnlock()
	return a.pageCache.slugs[strings.TrimPrefix(p, "/")]
}

func (v *View) FooterPages() []footerLink {
	v.App.pageCache.mu.RLock()
	defer v.App.pageCache.mu.RUnlock()
	return v.App.pageCache.footer[v.Lang]
}

// CustomPrivacy says whether the operator replaced the built-in privacy policy.
func (v *View) CustomPrivacy() bool {
	v.App.pageCache.mu.RLock()
	defer v.App.pageCache.mu.RUnlock()
	return v.App.pageCache.customPrivacy
}

// resolvePage finds a page by slug and prefers the visitor's own language when
// the page has a variant for it.
func (a *App) resolvePage(slug, lang string) (legalpages.Resolved, bool) {
	r, ok := a.pages().Resolve(slug)
	if !ok {
		return r, false
	}
	if r.Lang != lang {
		for _, alt := range r.Alternates {
			if alt.Lang == lang {
				if r2, ok := a.pages().Resolve(alt.Slug); ok {
					return r2, true
				}
			}
		}
	}
	return r, true
}

func (a *App) renderPage(w http.ResponseWriter, r *http.Request, p legalpages.Resolved) {
	a.render(w, r, "page", http.StatusOK, p.Title, map[string]any{
		"Title": p.Title, "Body": legalpages.SafeHTML(p.Body), "ShowUpdated": p.ShowUpdated, "UpdatedAt": p.UpdatedAt,
	})
}

// pagePublic serves an operator-written page at /{slug}.
func (a *App) pagePublic(w http.ResponseWriter, r *http.Request) {
	if a.isContactSlug(r.PathValue("slug")) {
		a.pageContact(w, r)
		return
	}
	if p, ok := a.resolvePage(r.PathValue("slug"), a.langOf(w, r)); ok {
		a.renderPage(w, r, p)
		return
	}
	a.errorPage(w, r, http.StatusNotFound, "err.not_found")
}

// pageView is a page with how many languages it still lacks.
type pageView struct {
	legalpages.Page
	Missing int
}

// ---- admin ----

func (a *App) pagesAdminData(r *http.Request, data map[string]any) error {
	langs := a.I18n.Langs()
	list, err := a.pages().ListPages(langs)
	if err != nil {
		return err
	}
	const per = 10
	offset, pg := paginate(r, "page", per, len(list), "")
	end := offset + per
	if end > len(list) {
		end = len(list)
	}
	data["Pager"] = pg
	list = list[offset:end]
	views := make([]pageView, 0, len(list))
	for _, p := range list {
		missing := 0
		for _, v := range p.Variants {
			if !v.Exists {
				missing++
			}
		}
		views = append(views, pageView{Page: p, Missing: missing})
	}
	data["PageList"], data["PageLangs"] = views, langs
	a.pageCache.mu.RLock()
	data["PrivacyCustom"] = a.pageCache.customPrivacy
	a.pageCache.mu.RUnlock()
	return nil
}

func boolOf(in map[string]any, key string) bool {
	switch v := in[key].(type) {
	case bool:
		return v
	case string:
		return v == "1" || v == "on" || v == "true"
	}
	return false
}

func strOf(in map[string]any, key string) string {
	s, _ := in[key].(string)
	return s
}

// variantsFrom reads the languages a form posted fields for; a language the form
// did not mention is left alone.
func (a *App) variantsFrom(in map[string]any) []legalpages.VariantInput {
	var out []legalpages.VariantInput
	for _, l := range a.I18n.Langs() {
		_, s1 := in["slug_"+l]
		_, s2 := in["title_"+l]
		_, s3 := in["body_"+l]
		if !s1 && !s2 && !s3 {
			continue
		}
		out = append(out, legalpages.VariantInput{
			Lang: l, Slug: strOf(in, "slug_"+l), Title: strOf(in, "title_"+l), Body: strOf(in, "body_"+l),
			Enabled: boolOf(in, "enabled_"+l), Delete: boolOf(in, "del_"+l),
		})
	}
	return out
}

func (a *App) pageErr(err error) error {
	var taken legalpages.ErrSlugTaken
	var bad legalpages.ErrBadSlug
	switch {
	case errors.As(err, &taken):
		return apiErr{http.StatusConflict, "err.page_slug_taken"}
	case errors.As(err, &bad):
		return apiErr{http.StatusBadRequest, "err.page_slug_bad"}
	}
	return err
}

// privacyDefault is the built-in privacy policy as Markdown for one language,
// ready to be edited.
func (a *App) privacyDefault(lang string) (title, body string) {
	operator := a.Cfg.OperatorName
	if operator == "" {
		operator = a.I18n.T(lang, "privacy.operator_default")
	}
	contact := a.Cfg.PrivacyContact
	if contact == "" {
		contact = a.I18n.T(lang, "privacy.contact_none")
	}
	var b strings.Builder
	b.WriteString(a.I18n.TN(lang, "privacy.intro", map[string]string{"app": a.Cfg.AppName, "operator": operator}) + "\n\n")
	for _, s := range privacySections {
		b.WriteString("## " + a.I18n.T(lang, "privacy."+s+".title") + "\n\n")
		if s == "contact" {
			b.WriteString(a.I18n.TN(lang, "privacy.contact.body", map[string]string{"operator": operator, "contact": contact}) + "\n\n")
		} else {
			b.WriteString(a.I18n.T(lang, "privacy."+s+".body") + "\n\n")
		}
	}
	return a.I18n.T(lang, "privacy.title"), strings.TrimSpace(b.String())
}

func (a *App) apiAdminPages(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in map[string]any
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	actor := admin.Username
	store := a.pages()
	slugs := []string{strOf(in, "slug")}
	for _, l := range a.I18n.Langs() {
		slugs = append(slugs, strOf(in, "slug_"+l))
	}
	for _, sl := range slugs {
		if sl = strings.ToLower(strings.TrimSpace(sl)); sl != "" && a.isContactSlug(sl) {
			a.fail(w, r, apiErr{http.StatusConflict, "err.page_slug_taken"})
			return
		}
	}
	pid := func() int {
		switch v := in["page_id"].(type) {
		case float64:
			return int(v)
		case string:
			n, _ := strconv.Atoi(v)
			return n
		}
		return 0
	}
	switch strOf(in, "action") {
	case "add":
		vars := a.variantsFrom(in)
		if lang := strOf(in, "lang"); lang != "" {
			if !a.I18n.Has(lang) {
				a.fail(w, r, errBadRequest)
				return
			}
			vars = []legalpages.VariantInput{{Lang: lang, Slug: strOf(in, "slug"), Title: strOf(in, "title"), Body: strOf(in, "body"), Enabled: boolOf(in, "enabled")}}
		}
		if len(vars) == 0 {
			a.fail(w, r, errBadRequest)
			return
		}
		if _, err := store.AddPage(vars, actor); err != nil {
			a.fail(w, r, a.pageErr(err))
			return
		}
	case "add_lang":
		lang := strOf(in, "lang")
		if !a.I18n.Has(lang) || pid() == 0 {
			a.fail(w, r, errBadRequest)
			return
		}
		list, err := store.ListPages(a.I18n.Langs())
		if err != nil {
			a.fail(w, r, err)
			return
		}
		found := false
		for _, p := range list {
			if p.ID != pid() {
				continue
			}
			found = true
			v := legalpages.VariantInput{Lang: lang, Slug: strOf(in, "slug"), Title: strOf(in, "title"), Body: strOf(in, "body"), Enabled: boolOf(in, "enabled")}
			if err := store.SavePage(p.ID, p.Enabled, p.ShowUpdated, []legalpages.VariantInput{v}, actor); err != nil {
				a.fail(w, r, a.pageErr(err))
				return
			}
		}
		if !found {
			a.fail(w, r, errNotFound)
			return
		}
	case "save":
		if pid() == 0 {
			a.fail(w, r, errBadRequest)
			return
		}
		if err := store.SavePage(pid(), boolOf(in, "page_enabled"), boolOf(in, "show_updated"), a.variantsFrom(in), actor); err != nil {
			a.fail(w, r, a.pageErr(err))
			return
		}
	case "delete":
		if pid() == 0 {
			a.fail(w, r, errBadRequest)
			return
		}
		if err := store.DeletePage(pid()); err != nil {
			a.fail(w, r, err)
			return
		}
	case "privacy_customize":
		if _, ok := store.Resolve("privacy"); ok {
			a.fail(w, r, errBadRequest)
			return
		}
		var vars []legalpages.VariantInput
		for _, l := range a.I18n.Langs() {
			slug := "privacy"
			if l != "en" {
				slug = "privacy-" + l
			}
			title, body := a.privacyDefault(l)
			vars = append(vars, legalpages.VariantInput{Lang: l, Slug: slug, Title: title, Body: body, Enabled: true})
		}
		if _, err := store.AddPage(vars, actor); err != nil {
			a.fail(w, r, a.pageErr(err))
			return
		}
	case "privacy_restore":
		res, ok := store.Resolve("privacy")
		if !ok {
			a.fail(w, r, errBadRequest)
			return
		}
		if err := store.DeletePage(res.PageID); err != nil {
			a.fail(w, r, err)
			return
		}
	default:
		a.fail(w, r, errBadRequest)
		return
	}
	a.reloadPages()
	logger.Info("ADMIN uid=%d pages action=%s", admin.ID, strOf(in, "action"))
	a.okJSON(w, nil)
}
