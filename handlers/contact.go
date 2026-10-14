package handlers

import (
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"tvt/auth"
	"tvt/captcha"
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
// the address in any other language redirects there. It offers email, the
// Telegram bot's support chat, and a form guarded by a captcha.
func (a *App) pageContact(w http.ResponseWriter, r *http.Request) {
	own := a.contactSlug(a.langOf(w, r))
	if want := "/" + own; r.URL.Path != want {
		http.Redirect(w, r, want, http.StatusMovedPermanently)
		return
	}
	email := a.contactTo()
	bot := ""
	if a.Bot.Configured() {
		bot = a.Bot.Username
	}
	mailOK := email != "" && a.Mail != nil && a.Mail.Configured()
	formOK := mailOK || len(a.contactChats()) > 0
	data := map[string]any{"Email": email, "Bot": bot, "TelegramURL": template.URL("tg://resolve?domain=" + url.QueryEscape(bot) + "&start=support"), "FormAvailable": formOK, "PrivacyContact": a.Cfg.PrivacyContact}
	if formOK {
		ip := auth.ClientIP(r)
		id, img, _, err := captcha.Generate(a.Auth.CSRFToken(w, r), ip)
		if err != nil {
			logger.Error("CONTACT captcha: %v", err)
			data["FormAvailable"] = false
		} else {
			data["CaptchaID"], data["CaptchaImage"] = id, template.URL(img)
		}
	}
	a.page(w, r, "contact", "contact.title", data)
}

func (a *App) apiCaptchaNew(w http.ResponseWriter, r *http.Request) {
	id, img, _, err := captcha.Generate(a.Auth.CSRFToken(w, r), auth.ClientIP(r))
	if err != nil || img == "" {
		logger.Error("CAPTCHA new: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "image": img})
}

func (a *App) apiCaptchaVerify(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "expired": true})
		return
	}
	id := strings.TrimSpace(r.FormValue("captcha_id"))
	if id == "" {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "expired": true})
		return
	}
	committed := strings.TrimSpace(r.FormValue("committed")) != "0"
	ok, expired := captcha.Verify(id, strings.TrimSpace(r.FormValue("captcha_solution")), r.Header.Get("X-CSRF-Token"),
		strings.TrimSpace(r.FormValue("pow_nonce")), auth.ClientIP(r), committed)
	writeJSON(w, http.StatusOK, map[string]any{"success": ok, "expired": expired})
}

// contactChats are the Telegram chats a contact form message goes to: the
// support group when one is set, otherwise every admin who connected the bot.
func (a *App) contactChats() []string {
	if !a.Bot.Configured() {
		return nil
	}
	chats, err := a.supportOperators()
	if err != nil {
		logger.Error("CONTACT could not look up the support chats: %v", err)
		return nil
	}
	return chats
}

// apiContact sends a visitor's message to the operator once the captcha has
// been solved: to Telegram when the bot has somewhere to send it, for the
// fastest answer, otherwise by email.
func (a *App) apiContact(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name, Contact, Email, Message, Website string
		CaptchaID                              string `json:"captcha_id"`
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
	if !captcha.IsSolved(in.CaptchaID) {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.captcha_failed"})
		return
	}
	name, handle, msg := cleanChatText(in.Name, 100), cleanChatText(in.Contact, 100), strings.TrimSpace(in.Message)
	email := ""
	if strings.TrimSpace(in.Email) != "" {
		e, ok := normEmail(in.Email)
		if !ok {
			a.fail(w, r, errBadRequest)
			return
		}
		email = e
	}
	if name == "" || handle == "" || msg == "" || utf8.RuneCountInString(msg) > 5000 {
		a.fail(w, r, errBadRequest)
		return
	}

	if chats := a.contactChats(); len(chats) > 0 {
		text := "📨 Contact form from " + name + "\nContact: " + handle
		if email != "" {
			text += "\nEmail: " + email
		}
		text += "\n\n" + msg
		delivered := false
		for _, chat := range chats {
			if _, err := a.tgSend(chat, text); err != nil {
				logger.Error("CONTACT telegram delivery to chat=%s from %q failed: %v", chat, name, err)
				continue
			}
			delivered = true
		}
		if delivered {
			logger.Info("CONTACT message delivered to Telegram")
			a.okJSON(w, nil)
			return
		}
		logger.Error("CONTACT no Telegram chat accepted the message from %q", name)
	}

	to := a.contactTo()
	if to == "" || a.Mail == nil || !a.Mail.Configured() {
		a.fail(w, r, apiErr{http.StatusServiceUnavailable, "err.contact_unavailable"})
		return
	}
	subject := a.Cfg.AppName + " contact: " + strings.NewReplacer("\r", " ", "\n", " ").Replace(name)
	body := "From: " + name + "\nContact: " + handle
	if email != "" {
		body += "\nEmail: " + email
	}
	body += "\n\n" + msg + "\n"
	if err := a.Mail.Send(to, subject, body); err != nil {
		logger.Error("CONTACT could not email a message from %q: %v", name, err)
		a.fail(w, r, errMailDown)
		return
	}
	logger.Info("CONTACT message delivered to the operator by email")
	a.okJSON(w, nil)
}
