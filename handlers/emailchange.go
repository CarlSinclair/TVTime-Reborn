package handlers

import (
	"errors"
	"net/http"
	"strings"
	"tvt/i18n"

	"github.com/CarlSinclair/csuit-libs/emailchange"

	"tvt/auth"
	"tvt/logger"
)

func maskEmail(e string) string {
	at := strings.LastIndex(e, "@")
	if at < 1 {
		return e
	}
	return e[:1] + "***" + e[at:]
}

func (a *App) emailChanges() *emailchange.SQLStore {
	return &emailchange.SQLStore{DB: a.Store.DB, TextTime: true, EmptyNotNull: true}
}

func (a *App) emailChangeErr(err error) error {
	switch {
	case errors.Is(err, emailchange.ErrInvalidEmail):
		return apiErr{http.StatusBadRequest, "err.invalid_email"}
	case errors.Is(err, emailchange.ErrSameEmail):
		return apiErr{http.StatusBadRequest, "err.email_same"}
	case errors.Is(err, emailchange.ErrTaken):
		return apiErr{http.StatusConflict, "err.email_taken"}
	case errors.Is(err, emailchange.ErrNotFound), errors.Is(err, emailchange.ErrExpired), errors.Is(err, emailchange.ErrSession):
		return apiErr{http.StatusBadRequest, "err.link_invalid"}
	}
	return err
}

func (a *App) apiChangeEmail(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Email, Code string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
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
	if err := a.allow(a.mfaLimit, "mfa-email:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.requireSecondFactor(u, in.Code); err != nil {
		a.fail(w, r, err)
		return
	}
	lang := u.Locale
	if !a.I18n.Has(lang) {
		lang = i18n.Fallback
	}
	send := func(to, link string) error {
		return a.Mail.Send(to, a.I18n.T(lang, "mail.emailchange.subject", a.Cfg.AppName), a.I18n.T(lang, "mail.emailchange.body", a.Cfg.AppName, link))
	}
	p, err := emailchange.Start(r.Context(), a.emailChanges(), u.ID, u.Email, in.Email, emailchange.Options{}, a.Cfg.BaseURL, "/t/"+auth.PurposeEmail, send)
	if err != nil {
		logger.Error("ACCOUNT email change request uid=%d: %v", u.ID, err)
		if mapped := a.emailChangeErr(err); mapped != err {
			a.fail(w, r, mapped)
		} else {
			a.fail(w, r, errMailDown)
		}
		return
	}
	a.audit(r, "ACCOUNT email_change_requested uid=%d", u.ID)
	if u.Email != "" {
		a.Notif.Send(u.ID, "email_change_requested", "/settings?tab=account", map[string]any{"email": maskEmail(p.NewEmail)})
	}
	a.okJSON(w, nil)
}

func (a *App) apiEmailChangeConsume(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.authLimit, "tok:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	var sessionUID int64
	if cur := auth.UserFrom(r.Context()); cur != nil {
		sessionUID = cur.ID
	}
	before := ""
	if p, err := emailchange.Peek(r.Context(), a.emailChanges(), strings.TrimSpace(in.Token), sessionUID); err == nil {
		if u, uerr := a.Store.UserByID(p.UserID); uerr == nil {
			before = u.Email
		} else {
			logger.Error("ACCOUNT email change uid=%d: read current address: %v", p.UserID, uerr)
		}
	}
	p, err := emailchange.Confirm(r.Context(), a.emailChanges(), strings.TrimSpace(in.Token), sessionUID, emailchange.Options{})
	if err != nil {
		if mapped := a.emailChangeErr(err); mapped != err {
			logger.Warn("ACCOUNT email change confirm refused uid=%d: %v", p.UserID, err)
			a.fail(w, r, mapped)
		} else {
			logger.Error("ACCOUNT email change confirm failed uid=%d: %v", p.UserID, err)
			a.fail(w, r, err)
		}
		return
	}
	uid := p.UserID
	if err := a.Store.SetEmailVerified(uid); err != nil {
		logger.Error("ACCOUNT email changed uid=%d: mark verified: %v", uid, err)
	}
	a.audit(r, "ACCOUNT email_changed uid=%d", uid)
	a.promoteIfAdmin(uid)
	u, err := a.Store.UserByID(uid)
	if err != nil {
		logger.Error("ACCOUNT email changed uid=%d: reload: %v", uid, err)
	} else {
		lang := u.Locale
		if !a.I18n.Has(lang) {
			lang = i18n.Fallback
		}
		if before != "" {
			if err := a.Mail.Send(before, a.I18n.T(lang, "mail.emailchanged.subject", a.Cfg.AppName), a.I18n.T(lang, "mail.emailchanged.body", a.Cfg.AppName, maskEmail(p.NewEmail))); err != nil {
				logger.Error("ACCOUNT email changed notice to previous address uid=%d: %v", uid, err)
			}
		}
		a.Notif.Send(uid, "email_changed", "/settings?tab=account", map[string]any{"email": maskEmail(p.NewEmail)})
	}
	a.okJSON(w, map[string]any{"redirect": "/settings?tab=account"})
}
