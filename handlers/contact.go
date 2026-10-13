package handlers

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"tvt/auth"
	"tvt/legalpages"
	"tvt/logger"
)

// contactTo is where the contact form's messages go: the address set in
// Admin > Config, or empty when none is set.
func (a *App) contactTo() string {
	return strings.TrimSpace(a.Cfg.ContactEmail)
}

// contactSlug is the contact page's address in one language: the "contact.slug"
// key of that language's locale file, or "contact" when it is missing or not a
// valid slug. A new locale gets its own address by filling in that key.
func (a *App) contactSlug(lang string) string {
	slug := strings.ToLower(strings.TrimSpace(a.I18n.T(lang, "contact.slug")))
	if !legalpages.SlugRe.MatchString(slug) {
		return "contact"
	}
	return slug
}

// isContactSlug says whether a path segment is the contact page in any language.
func (a *App) isContactSlug(slug string) bool {
	slug = strings.TrimPrefix(slug, "/")
	for _, l := range a.I18n.Langs() {
		if a.contactSlug(l) == slug {
			return true
		}
	}
	return false
}

func (v *View) ContactSlug() string { return v.App.contactSlug(v.Lang) }

// pageContact serves the contact page at its address in the visitor's language;
// the address in any other language redirects there.
func (a *App) pageContact(w http.ResponseWriter, r *http.Request) {
	own := a.contactSlug(a.langOf(w, r))
	if want := "/" + own; r.URL.Path != want {
		http.Redirect(w, r, want, http.StatusMovedPermanently)
		return
	}
	a.page(w, r, "contact", "contact.title", map[string]any{"Available": a.contactTo() != "" && a.Mail != nil && a.Mail.Configured(), "PrivacyContact": a.Cfg.PrivacyContact})
}

// apiContact sends a visitor's message to the operator by email.
func (a *App) apiContact(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name, Email, Message, Website string
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.Website != "" {
		a.okJSON(w, nil)
		return
	}
	if err := a.allow(a.contactLimit, "contact:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	name, msg := strings.TrimSpace(in.Name), strings.TrimSpace(in.Message)
	email, ok := normEmail(in.Email)
	if name == "" || msg == "" || !ok || utf8.RuneCountInString(name) > 100 || utf8.RuneCountInString(msg) > 5000 {
		a.fail(w, r, errBadRequest)
		return
	}
	to := a.contactTo()
	if to == "" || a.Mail == nil || !a.Mail.Configured() {
		a.fail(w, r, apiErr{http.StatusServiceUnavailable, "err.contact_unavailable"})
		return
	}
	subject := a.Cfg.AppName + " contact: " + strings.NewReplacer("\r", " ", "\n", " ").Replace(name)
	body := "From: " + name + " <" + email + ">\n\n" + msg + "\n"
	if err := a.Mail.Send(to, subject, body); err != nil {
		logger.Error("CONTACT could not send a message from %s: %v", email, err)
		a.fail(w, r, errMailDown)
		return
	}
	logger.Info("CONTACT message delivered to the operator")
	a.okJSON(w, nil)
}
