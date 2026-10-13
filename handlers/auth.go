package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"tvt/auth"
	"tvt/logger"
	"tvt/store"
)

func normEmail(e string) (string, bool) {
	e = strings.ToLower(strings.TrimSpace(e))
	if len(e) > 254 {
		return "", false
	}
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e || !strings.Contains(e[strings.LastIndex(e, "@"):], ".") {
		return "", false
	}
	return e, true
}

func (a *App) sendLink(u *store.User, purpose string) error {
	tok, err := a.Auth.IssueEmailToken(u.ID, purpose)
	if err != nil {
		return err
	}
	link := a.Cfg.BaseURL + "/t/" + purpose + "?token=" + url.QueryEscape(tok)
	lang := u.Locale
	if !a.I18n.Has(lang) {
		lang = a.Cfg.DefaultLang
	}
	subject := a.I18n.T(lang, "mail."+purpose+".subject", a.Cfg.AppName)
	body := a.I18n.T(lang, "mail."+purpose+".body", a.Cfg.AppName, link)
	if err := a.Mail.Send(u.Email, subject, body); err != nil {
		logger.Error("AUTH send %s link uid=%d: %v", purpose, u.ID, err)
		return errMailDown
	}
	return nil
}

func (a *App) apiRegister(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Username, Password string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.authLimit, "reg:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	email, ok := normEmail(in.Email)
	if !ok {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.invalid_email"})
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if !store.ValidUsername(in.Username) {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.invalid_username"})
		return
	}
	if store.ReservedUsername(in.Username) {
		a.fail(w, r, apiErr{http.StatusConflict, "err.username_taken"})
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		if errors.Is(err, auth.ErrPasswordRules) {
			a.fail(w, r, apiErr{http.StatusBadRequest, "err.weak_password"})
			return
		}
		a.fail(w, r, err)
		return
	}
	lang := a.lang(w, r, nil)
	u, err := a.Store.CreateUser(email, in.Username, hash, lang, false)
	switch {
	case errors.Is(err, store.ErrEmailTaken):
		a.fail(w, r, apiErr{http.StatusConflict, "err.email_taken"})
		return
	case errors.Is(err, store.ErrUsernameTaken):
		a.fail(w, r, apiErr{http.StatusConflict, "err.username_taken"})
		return
	case err != nil:
		a.fail(w, r, err)
		return
	}
	if err := a.Auth.StartSession(w, u.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	mailSent := true
	if err := a.sendLink(u, auth.PurposeVerify); err != nil {
		mailSent = false
	}
	logger.Info("AUTH registered uid=%d username=%s mail_sent=%v", u.ID, u.Username, mailSent)
	a.okJSON(w, map[string]any{"redirect": "/", "mail_sent": mailSent})
}

func (a *App) apiLogin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.authLimit, "login:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	u, err := a.Auth.Login(in.Email, in.Password)
	switch {
	case errors.Is(err, auth.ErrBadCredentials):
		logger.Warn("AUTH login failed from=%s", auth.ClientCountry(r))
		a.fail(w, r, apiErr{http.StatusUnauthorized, "err.bad_credentials"})
		return
	case errors.Is(err, auth.ErrBanned):
		a.fail(w, r, apiErr{http.StatusForbidden, "err.banned"})
		return
	case err != nil:
		a.fail(w, r, err)
		return
	}
	a.finishLogin(w, r, u)
}

func (a *App) apiLogout(w http.ResponseWriter, r *http.Request) {
	if err := a.Auth.EndSession(w, r); err != nil {
		logger.Error("AUTH logout: %v", err)
	}
	a.okJSON(w, map[string]any{"redirect": "/"})
}

func (a *App) apiMagicRequest(w http.ResponseWriter, r *http.Request) {
	a.emailLinkRequest(w, r, auth.PurposeLogin)
}

func (a *App) apiForgot(w http.ResponseWriter, r *http.Request) {
	a.emailLinkRequest(w, r, auth.PurposeReset)
}

func (a *App) emailLinkRequest(w http.ResponseWriter, r *http.Request, purpose string) {
	var in struct{ Email string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	email, ok := normEmail(in.Email)
	if !ok {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.invalid_email"})
		return
	}
	if err := a.allow(a.mailLimit, purpose+":"+email); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.authLimit, "mail:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	if !a.Mail.Configured() && !a.Cfg.DevLogMail {
		logger.Error("AUTH %s link requested but mail is not configured", purpose)
		a.fail(w, r, errMailDown)
		return
	}
	u, err := a.Store.UserByEmail(email)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, err)
			return
		}
		a.okJSON(w, nil)
		return
	}
	if u.IsBanned {
		a.okJSON(w, nil)
		return
	}
	if err := a.sendLink(u, purpose); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

func (a *App) apiResendVerify(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if u.EmailVerified {
		a.okJSON(w, nil)
		return
	}
	if err := a.allow(a.mailLimit, "verify:"+u.Email); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.sendLink(u, auth.PurposeVerify); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

func (a *App) consume(w http.ResponseWriter, r *http.Request, purpose string) (int64, string, bool) {
	var in struct{ Token, Password string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return 0, "", false
	}
	if err := a.allow(a.authLimit, "tok:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return 0, "", false
	}
	if purpose == auth.PurposeReset {
		if err := auth.ValidPassword(in.Password); err != nil {
			a.fail(w, r, apiErr{http.StatusBadRequest, "err.weak_password"})
			return 0, "", false
		}
	}
	uid, err := a.Auth.ConsumeEmailToken(in.Token, purpose)
	if err != nil {
		if errors.Is(err, auth.ErrBadToken) {
			a.fail(w, r, apiErr{http.StatusBadRequest, "err.link_invalid"})
		} else {
			a.fail(w, r, err)
		}
		return 0, "", false
	}
	return uid, in.Password, true
}

func (a *App) apiVerifyConsume(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.consume(w, r, auth.PurposeVerify)
	if !ok {
		return
	}
	if err := a.Store.SetEmailVerified(uid); err != nil {
		a.fail(w, r, err)
		return
	}
	a.promoteIfAdmin(uid)
	a.okJSON(w, map[string]any{"redirect": "/"})
}

func (a *App) apiMagicConsume(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.consume(w, r, auth.PurposeLogin)
	if !ok {
		return
	}
	u, err := a.Store.UserByID(uid)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if u.IsBanned {
		a.fail(w, r, apiErr{http.StatusForbidden, "err.banned"})
		return
	}
	if !u.EmailVerified {
		// Whoever registered this address before its owner proved control of it
		// may know the password; the owner's proof of control wipes it and every
		// session that was opened with it.
		if err := a.Store.SetPassword(uid, ""); err != nil {
			a.fail(w, r, err)
			return
		}
		if err := a.Auth.RevokeAll(uid); err != nil {
			a.fail(w, r, err)
			return
		}
		logger.Warn("AUTH unverified account claimed via email link uid=%d: password cleared, sessions revoked", uid)
	}
	if err := a.Store.SetEmailVerified(uid); err != nil {
		a.fail(w, r, err)
		return
	}
	u.EmailVerified = true
	a.promoteIfAdmin(uid)
	a.finishLogin(w, r, u)
}

func (a *App) apiResetConsume(w http.ResponseWriter, r *http.Request) {
	uid, pw, ok := a.consume(w, r, auth.PurposeReset)
	if !ok {
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Store.SetPassword(uid, hash); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Store.SetEmailVerified(uid); err != nil {
		a.fail(w, r, err)
		return
	}
	a.promoteIfAdmin(uid)
	if err := a.Auth.RevokeAll(uid); err != nil {
		a.fail(w, r, err)
		return
	}
	logger.Info("AUTH password reset uid=%d", uid)
	a.okJSON(w, map[string]any{"redirect": "/login"})
}

func (a *App) apiSetLocale(w http.ResponseWriter, r *http.Request) {
	var in struct{ Locale string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if !a.I18n.Has(in.Locale) {
		a.fail(w, r, errBadRequest)
		return
	}
	u := auth.UserFrom(r.Context())
	if u == nil {
		a.fail(w, r, errUnauth)
		return
	}
	if err := a.Store.SetLocale(u.ID, in.Locale); err != nil {
		a.fail(w, r, err)
		return
	}
	a.Sync.PrefetchLanguages()
	a.okJSON(w, nil)
}

func (a *App) apiChangePassword(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Current, New string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.authLimit, "pw:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	if u.HasPassword() && !a.Auth.CheckPassword(u, in.Current) {
		a.fail(w, r, apiErr{http.StatusUnauthorized, "err.bad_credentials"})
		return
	}
	hash, err := auth.HashPassword(in.New)
	if err != nil {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.weak_password"})
		return
	}
	if err := a.Store.SetPassword(u.ID, hash); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Auth.RevokeAll(u.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Auth.StartSession(w, u.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.Notif.Send(u.ID, "password_changed", "/settings?tab=account", nil)
	a.okJSON(w, nil)
}

func (a *App) apiAddEmail(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Email string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	email, ok := normEmail(in.Email)
	if !ok {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.invalid_email"})
		return
	}
	if u.Email != "" {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.allow(a.mailLimit, "addmail:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Store.AddEmail(u.ID, email); err != nil {
		if errors.Is(err, store.ErrEmailTaken) {
			a.fail(w, r, apiErr{http.StatusConflict, "err.email_taken"})
		} else {
			a.fail(w, r, err)
		}
		return
	}
	fresh, err := a.Store.UserByID(u.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.sendLink(fresh, auth.PurposeVerify); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

func (a *App) apiSetTimezone(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Timezone string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil || in.Timezone == "" || in.Timezone == "Local" {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.invalid_timezone"})
		return
	}
	if err := a.Store.SetTimezone(u.ID, in.Timezone); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

func (a *App) apiDeleteAccount(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Password, Confirm string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if u.HasPassword() {
		if !a.Auth.CheckPassword(u, in.Password) {
			a.fail(w, r, apiErr{http.StatusUnauthorized, "err.bad_credentials"})
			return
		}
	} else if !strings.EqualFold(strings.TrimSpace(in.Confirm), u.Username) {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.confirm_username"})
		return
	}
	if err := a.Store.DeleteUser(u.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.removeUserMedia(r.Context(), u); err != nil {
		logger.Error("ACCOUNT delete media uid=%d: %v", u.ID, err)
	}
	a.Auth.ClearCookies(w)
	logger.Info("ACCOUNT deleted uid=%d", u.ID)
	a.okJSON(w, map[string]any{"redirect": "/"})
}

func (a *App) apiExport(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out, err := a.Store.ExportUser(u.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-export.json"`, u.Username))
	writeJSON(w, http.StatusOK, out)
}

// promoteIfAdmin grants admin only to the configured admin address, and only
// once that address has been verified by mail, so nobody can claim it by
// registering first.
func (a *App) promoteIfAdmin(uid int64) {
	if a.Cfg.AdminEmail == "" {
		return
	}
	u, err := a.Store.UserByID(uid)
	if err != nil {
		logger.Error("AUTH admin promotion lookup uid=%d: %v", uid, err)
		return
	}
	if u.IsAdmin || !u.EmailVerified || !strings.EqualFold(u.Email, a.Cfg.AdminEmail) {
		return
	}
	if err := a.Store.SetAdmin(uid); err != nil {
		logger.Error("AUTH admin promotion uid=%d: %v", uid, err)
		return
	}
	logger.Info("AUTH admin granted uid=%d", uid)
}
