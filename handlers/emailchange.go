package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"tvt/i18n"

	"tvt/auth"
	"tvt/logger"
	"tvt/store"
)

func maskEmail(e string) string {
	at := strings.LastIndex(e, "@")
	if at < 1 {
		return e
	}
	return e[:1] + "***" + e[at:]
}

func (a *App) apiChangeEmail(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Email, Password, Code string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	email, ok := normEmail(in.Email)
	if !ok {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.invalid_email"})
		return
	}
	if strings.EqualFold(email, u.Email) {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.email_same"})
		return
	}
	if err := a.allow(a.mailLimit, "chgmail:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	if !a.Mail.Configured() && !a.Cfg.DevLogMail {
		logger.Error("ACCOUNT email change requested uid=%d but mail is not configured", u.ID)
		a.fail(w, r, errMailDown)
		return
	}
	if u.HasPassword() {
		if err := a.requireReauth(u, in.Password, ""); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	if u.TOTPEnabled {
		if err := a.allow(a.mfaLimit, "mfa-email:"+itoa(u.ID)); err != nil {
			a.fail(w, r, err)
			return
		}
		if err := a.Auth.VerifySecondFactor(u, in.Code); err != nil {
			a.fail(w, r, apiErr{http.StatusUnauthorized, "err.mfa_bad_code"})
			return
		}
	}
	if err := a.Store.SetPendingEmail(u.ID, email); err != nil {
		if errors.Is(err, store.ErrEmailTaken) {
			a.fail(w, r, apiErr{http.StatusConflict, "err.email_taken"})
		} else {
			a.fail(w, r, err)
		}
		return
	}
	tok, err := a.Auth.IssueEmailToken(u.ID, auth.PurposeEmail)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	lang := u.Locale
	if !a.I18n.Has(lang) {
		lang = i18n.Fallback
	}
	link := a.Cfg.BaseURL + "/t/" + auth.PurposeEmail + "?token=" + url.QueryEscape(tok)
	if err := a.Mail.Send(email, a.I18n.T(lang, "mail.emailchange.subject", a.Cfg.AppName), a.I18n.T(lang, "mail.emailchange.body", a.Cfg.AppName, link)); err != nil {
		logger.Error("ACCOUNT email change mail uid=%d: %v", u.ID, err)
		a.fail(w, r, errMailDown)
		return
	}
	logger.Info("ACCOUNT email change requested uid=%d", u.ID)
	if u.Email != "" {
		a.Notif.Send(u.ID, "email_change_requested", "/settings?tab=account", map[string]any{"email": maskEmail(email)})
	}
	a.okJSON(w, nil)
}

func (a *App) apiEmailChangeConsume(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.consume(w, r, auth.PurposeEmail)
	if !ok {
		return
	}
	oldEmail, newEmail, err := a.Store.ConfirmEmailChange(uid)
	switch {
	case errors.Is(err, store.ErrNotFound):
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.link_invalid"})
		return
	case errors.Is(err, store.ErrEmailTaken):
		a.fail(w, r, apiErr{http.StatusConflict, "err.email_taken"})
		return
	case err != nil:
		a.fail(w, r, err)
		return
	}
	logger.Info("ACCOUNT email changed uid=%d", uid)
	a.promoteIfAdmin(uid)
	if u, err := a.Store.UserByID(uid); err != nil {
		logger.Error("ACCOUNT email changed uid=%d: reload: %v", uid, err)
	} else {
		lang := u.Locale
		if !a.I18n.Has(lang) {
			lang = i18n.Fallback
		}
		if oldEmail != "" {
			if err := a.Mail.Send(oldEmail, a.I18n.T(lang, "mail.emailchanged.subject", a.Cfg.AppName), a.I18n.T(lang, "mail.emailchanged.body", a.Cfg.AppName, maskEmail(newEmail))); err != nil {
				logger.Error("ACCOUNT email changed notice to previous address uid=%d: %v", uid, err)
			}
		}
		a.Notif.Send(uid, "email_changed", "/settings?tab=account", map[string]any{"email": maskEmail(newEmail)})
	}
	a.okJSON(w, map[string]any{"redirect": "/settings?tab=account"})
}
