package handlers

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tvt/auth"
	"tvt/i18n"
	"tvt/logger"
	"tvt/store"
	"tvt/telegram"

	"github.com/CarlSinclair/csuit-libs/redis"
	"github.com/CarlSinclair/csuit-libs/twofa"
	goredis "github.com/redis/go-redis/v9"
)

const twoFATenant = "tvt"

func twoFAChat(u *store.User) int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(u.TelegramChatID), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func (a *App) mailOK() bool {
	return a.Mail != nil && (a.Mail.Configured() || a.Cfg.DevLogMail)
}

func (a *App) twoFAMethods(u *store.User) (twofa.Prefs, []twofa.Method, error) {
	p, err := a.Store.TwoFAPrefs(u.ID)
	if err != nil {
		return twofa.Prefs{}, nil, err
	}
	enabled := p.Enabled(u.TOTPEnabled, twoFAChat(u) != 0 && a.Bot.Configured())
	if strings.TrimSpace(u.Email) == "" || !a.mailOK() {
		kept := enabled[:0:0]
		for _, m := range enabled {
			if m != twofa.MethodEmail {
				kept = append(kept, m)
			}
		}
		enabled = kept
	}
	return p, enabled, nil
}

func (a *App) twoFASignInMethods(u *store.User) (twofa.Prefs, []twofa.Method, error) {
	p, enabled, err := a.twoFAMethods(u)
	if err != nil {
		return p, enabled, err
	}
	n, err := a.Store.CountPasskeys(u.ID)
	if err != nil {
		logger.Error("AUTH 2fa passkey count uid=%d: %v", u.ID, err)
		return p, enabled, nil
	}
	if n > 0 {
		enabled = append(enabled, twofa.MethodPasskey)
	}
	return p, enabled, nil
}

func (a *App) twoFARequired(u *store.User, via twofa.Via) (bool, error) {
	p, enabled, err := a.twoFASignInMethods(u)
	if err != nil {
		return false, err
	}
	return p.NeedsStep(via, enabled), nil
}

func (a *App) sendTwoFAEmailLink(r *http.Request, u *store.User) (string, string, error) {
	if strings.TrimSpace(u.Email) == "" || !a.mailOK() {
		return "", "", errMailDown
	}
	token, secret, err := twofa.StartEmailApproval(twoFATenant, u.ID)
	if err != nil {
		return "", "", err
	}
	lang := u.Locale
	if !a.I18n.Has(lang) {
		lang = i18n.Fallback
	}
	link := a.Cfg.BaseURL + "/login/2fa/email/link?t=" + url.QueryEscape(token)
	body := a.I18n.T(lang, "tfa_mail_body") + "\n\n" + link + "\n\n" + a.I18n.T(lang, "tfa_mail_disclaimer")
	htmlBody := "<p>" + html.EscapeString(a.I18n.T(lang, "tfa_mail_body")) + "</p><p><a href=\"" + html.EscapeString(link) + "\">" + html.EscapeString(a.I18n.T(lang, "tfa_mail_button")) + "</a></p><p>" + html.EscapeString(a.I18n.T(lang, "tfa_mail_disclaimer")) + "</p>"
	if err := a.Mail.SendHTML(u.Email, a.I18n.T(lang, "tfa_mail_subject"), body, htmlBody); err != nil {
		twofa.DiscardEmailApproval(twoFATenant, u.ID, token)
		logger.Error("AUTH 2fa_email link send uid=%d: %v", u.ID, err)
		return "", "", errMailDown
	}
	return token, secret, nil
}

func (a *App) startTwoFATelegram(r *http.Request, u *store.User) (string, string, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	lang := u.Locale
	return twofa.StartApproval(ctx, a.Bot, twoFATenant, u.ID, twoFAChat(u), twofa.Texts{
		Prompt:  a.tgT(lang, "tg.tfa_prompt", nil),
		Approve: a.tgT(lang, "tg.tfa_approve", nil),
		Deny:    a.tgT(lang, "tg.tfa_deny", nil),
	})
}

func (a *App) loadTwoFAStep(r *http.Request) (*twofa.Step[*store.User], error) {
	uid, err := a.Auth.PendingMFAUser(r)
	if err != nil {
		return nil, apiErr{http.StatusUnauthorized, "err.mfa_expired"}
	}
	token := a.Auth.PendingMFAToken(r)
	u, err := a.Store.UserByID(uid)
	if err != nil {
		return nil, err
	}
	if u.IsDeactivated && !u.SelfDeactivated {
		return nil, apiErr{http.StatusForbidden, "err.deactivated"}
	}
	prefs, enabled, err := a.twoFASignInMethods(u)
	if err != nil {
		logger.Error("AUTH 2fa_step prefs uid=%d: %v", uid, err)
		return nil, err
	}
	step, err := twofa.NewStep(u, uid, token, prefs, enabled, u.HasPassword())
	if err != nil {
		logger.Error("AUTH 2fa_step progress read uid=%d: %v", uid, err)
		return nil, err
	}
	return step, nil
}

func (a *App) finishTwoFAMethod(w http.ResponseWriter, s *twofa.Step[*store.User], m twofa.Method) (bool, error) {
	complete, err := s.Finish(m)
	if err != nil {
		logger.Error("AUTH 2fa_step could not record method=%s uid=%d: %v", m, s.UserID, err)
		return false, err
	}
	if complete {
		a.Auth.ClearMFA(w)
	}
	return complete, nil
}

func (a *App) pageMFA(w http.ResponseWriter, r *http.Request) {
	s, err := a.loadTwoFAStep(r)
	if err != nil {
		var ae apiErr
		if !errors.As(err, &ae) {
			logger.Error("PAGE mfa: %v", err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	a.page(w, r, "mfa", "mfa.title", map[string]any{
		"TOTP":     s.Open(twofa.MethodTOTP),
		"Telegram": s.Open(twofa.MethodTelegram),
		"Email":    s.Open(twofa.MethodEmail),
		"Passkey":  s.Open(twofa.MethodPasskey),
		"Password": s.Open(twofa.MethodPassword),
	})
}

func (a *App) pageMFALink(w http.ResponseWriter, r *http.Request) {
	twofa.LinkHost{Page: func(w http.ResponseWriter, r *http.Request, state, token string) {
		a.page(w, r, "mfalink", "tfa_link_title", map[string]any{"State": state, "Token": token})
	}}.ServeHTTP(w, r)
}

func (a *App) twoFAStepHost() twofa.StepHost[*store.User] {
	return twofa.StepHost[*store.User]{
		Load: func(r *http.Request) (*twofa.Step[*store.User], string) {
			s, err := a.loadTwoFAStep(r)
			if err != nil {
				var ae apiErr
				if errors.As(err, &ae) {
					return nil, ae.key
				}
				return nil, "system_error"
			}
			return s, ""
		},
		StartTelegram: func(r *http.Request, s *twofa.Step[*store.User]) (string, string, error) {
			if err := a.allow(a.mfaLimit, "mfa-tg:"+itoa(s.UserID)); err != nil {
				return "", "", err
			}
			return a.startTwoFATelegram(r, s.User)
		},
		SendEmailLink: func(r *http.Request, s *twofa.Step[*store.User]) (string, string, error) {
			if err := a.allow(a.mfaLimit, "mfa-mail:"+itoa(s.UserID)); err != nil {
				return "", "", err
			}
			return a.sendTwoFAEmailLink(r, s.User)
		},
		Complete: func(w http.ResponseWriter, r *http.Request, s *twofa.Step[*store.User]) (string, string) {
			a.Auth.ClearMFA(w)
			if err := a.Auth.StartSessionFor(w, r, s.UserID); err != nil {
				logger.Error("AUTH 2fa_step start session uid=%d: %v", s.UserID, err)
				return "", "system_error"
			}
			return "/", ""
		},
		JSON: func(w http.ResponseWriter, r *http.Request, v any) {
			out, _ := v.(map[string]any)
			a.okJSON(w, out)
		},
		Fail: func(w http.ResponseWriter, r *http.Request, status int, key string) {
			if mapped, ok := twoFAConfirmErrKeys[key]; ok {
				key = mapped
			}
			a.fail(w, r, apiErr{status, key})
		},
	}
}

func (a *App) apiTwoFATelegramStart(w http.ResponseWriter, r *http.Request) {
	a.twoFAStepHost().TelegramStart(w, r)
}

func (a *App) apiTwoFAEmailStart(w http.ResponseWriter, r *http.Request) {
	a.twoFAStepHost().EmailStart(w, r)
}

func (a *App) apiTwoFAPoll(w http.ResponseWriter, r *http.Request) {
	a.twoFAStepHost().Poll(w, r)
}

func (a *App) apiTwoFAPassword(w http.ResponseWriter, r *http.Request) {
	var in struct{ Password string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	s, err := a.loadTwoFAStep(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !s.Open(twofa.MethodPassword) {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.allow(a.mfaLimit, "mfa-pw:"+itoa(s.UserID)); err != nil {
		a.fail(w, r, err)
		return
	}
	if !a.Auth.CheckPassword(s.User, in.Password) {
		logger.Warn("AUTH 2fa_password bad_password uid=%d from=%s", s.UserID, auth.ClientCountry(r))
		a.fail(w, r, apiErr{http.StatusUnauthorized, "err.bad_credentials"})
		return
	}
	complete, err := a.finishTwoFAMethod(w, s, twofa.MethodPassword)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !complete {
		a.okJSON(w, map[string]any{"redirect": "/login/2fa"})
		return
	}
	if err := a.Auth.StartSessionFor(w, r, s.UserID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "AUTH 2fa_ok uid=%d method=password from=%s", s.UserID, auth.ClientCountry(r))
	a.okJSON(w, map[string]any{"redirect": "/"})
}

func (a *App) apiTwoFAPrefs(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Password string
		Code     string
		Require  bool
		All      bool
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.mfaLimit, "mfa-prefs:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.requireReauth(u, in.Password, in.Code); err != nil {
		a.fail(w, r, err)
		return
	}
	if u.HasPassword() {
		if err := a.requireSecondFactor(u, in.Code); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	p := twofa.Prefs{Require: &in.Require, All: in.All}
	if err := a.Store.SetTwoFAPrefs(u.ID, p); err != nil {
		logger.Error("AUTH 2fa prefs save uid=%d: %v", u.ID, err)
		a.fail(w, r, apiErr{http.StatusInternalServerError, "err.tfa_prefs_failed"})
		return
	}
	a.audit(r, "AUTH 2fa_prefs uid=%d prefs=%s", u.ID, p.Encode())
	a.okJSON(w, nil)
}

func (a *App) telegramTwoFAAnswer(cq *telegram.CallbackQuery, lang string) {
	chatID := cq.Message.Chat.ID
	_, approved, accepted := twofa.HandleCallback(cq.Data, chatID)
	key := "tg.tfa_stale"
	switch {
	case accepted && approved:
		key = "tg.tfa_approved"
	case accepted:
		key = "tg.tfa_denied"
	}
	if _, err := a.tgSend(strconv.FormatInt(chatID, 10), a.tgT(lang, key, nil)); err != nil {
		logger.Error("TELEGRAM 2fa answer reply chat=%d: %v", chatID, err)
	}
}

func (a *App) requireSecondFactor(u *store.User, code string, proven ...twofa.Method) error {
	prefs, enabled, err := a.twoFAMethods(u)
	if err != nil {
		return err
	}
	if len(enabled) == 0 {
		return nil
	}
	hasTOTP := false
	for _, m := range enabled {
		if m == twofa.MethodTOTP {
			hasTOTP = true
		}
	}
	codeOK := false
	if hasTOTP && strings.TrimSpace(code) != "" {
		verr := a.Auth.VerifySecondFactor(u, code)
		switch {
		case verr == nil:
			codeOK = true
		case errors.Is(verr, auth.ErrBadMFACode):
		default:
			return verr
		}
	}
	ok, perr := prefs.ProofSatisfied(twoFATenant, u.ID, enabled, codeOK, proven...)
	if perr != nil {
		return fmt.Errorf("check second factor proof uid=%d: %w", u.ID, perr)
	}
	if !ok {
		return apiErr{http.StatusUnauthorized, "err.mfa_bad_code"}
	}
	return nil
}

const twoFAResetTenant = twoFATenant + "-reset"

func (a *App) resetSecondFactor(uid int64, code string) error {
	u, err := a.Store.UserByID(uid)
	if err != nil {
		logger.Error("AUTH password reset user lookup uid=%d: %v", uid, err)
		return err
	}
	_, enabled, err := a.twoFAMethods(u)
	if err != nil {
		logger.Error("AUTH password reset 2fa methods uid=%d: %v", uid, err)
		return err
	}
	if len(enabled) == 0 {
		return nil
	}
	if err := a.allow(a.mfaLimit, "mfa-reset:"+itoa(uid)); err != nil {
		return err
	}
	var proven []twofa.Method
	if len(enabled) == 1 && enabled[0] == twofa.MethodEmail {
		proven = append(proven, twofa.MethodEmail)
	}
	code = strings.TrimSpace(code)
	for _, m := range enabled {
		if m == twofa.MethodTelegram {
			ok, cerr := a.takeResetApproval(uid)
			if cerr != nil {
				logger.Error("AUTH password reset telegram approval check uid=%d: %v", uid, cerr)
				return cerr
			}
			if ok {
				proven = append(proven, twofa.MethodTelegram)
			}
		}
	}
	if err := a.requireSecondFactor(u, code, proven...); err != nil {
		logger.Warn("AUTH password reset second factor refused uid=%d: %v", uid, err)
		return err
	}
	return nil
}

func (a *App) apiResetCode(w http.ResponseWriter, r *http.Request) {
	email, ok := normEmail(r.URL.Query().Get("email"))
	if !ok {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.invalid_email"})
		return
	}
	if err := a.allow(a.authLimit, "mail:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.mailLimit, "resetcode:"+email); err != nil {
		a.fail(w, r, err)
		return
	}
	u, err := a.Store.UserByEmail(email)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			logger.Error("AUTH password reset code user lookup failed: %v", err)
			a.fail(w, r, err)
			return
		}
		a.okJSON(w, nil)
		return
	}
	_, enabled, err := a.twoFAMethods(u)
	if err != nil {
		logger.Error("AUTH password reset code methods uid=%d: %v", u.ID, err)
		a.fail(w, r, err)
		return
	}
	hasTG := false
	for _, m := range enabled {
		hasTG = hasTG || m == twofa.MethodTelegram
	}
	if !hasTG || (u.IsDeactivated && !u.SelfDeactivated) {
		a.okJSON(w, nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	lang := u.Locale
	token, secret, err := twofa.StartApproval(ctx, a.Bot, twoFAResetTenant, u.ID, twoFAChat(u), twofa.Texts{
		Prompt:  a.tgT(lang, "tg.reset_prompt", nil),
		Approve: a.tgT(lang, "tg.tfa_approve", nil),
		Deny:    a.tgT(lang, "tg.tfa_deny", nil),
	})
	if err != nil {
		logger.Error("AUTH password reset telegram approval send uid=%d: %v", u.ID, err)
		a.fail(w, r, apiErr{http.StatusBadGateway, "mfa.tg_failed"})
		return
	}
	if err := redis.Client().Set(redis.Ctx, resetApprovalKey(u.ID), token+":"+secret, 10*time.Minute).Err(); err != nil {
		logger.Error("AUTH password reset approval not stored uid=%d: %v", u.ID, err)
		a.fail(w, r, apiErr{http.StatusInternalServerError, "err.internal"})
		return
	}
	a.okJSON(w, nil)
}

func resetApprovalKey(uid int64) string { return "twofa-reset:" + itoa(uid) }

func (a *App) takeResetApproval(uid int64) (bool, error) {
	if redis.Client() == nil {
		return false, twofa.ErrNoRedis
	}
	val, err := redis.Client().Get(redis.Ctx, resetApprovalKey(uid)).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return false, nil
		}
		return false, err
	}
	token, secret, found := strings.Cut(val, ":")
	if !found {
		return false, nil
	}
	status, approvedUID, _ := twofa.PollApprovalMethod(token, secret)
	if status != "approved" || approvedUID != uid {
		return false, nil
	}
	if err := redis.Client().Del(redis.Ctx, resetApprovalKey(uid)).Err(); err != nil {
		return false, err
	}
	return true, nil
}

var twoFAConfirmErrKeys = map[string]string{
	"unauthorized":              "err.login_required",
	"bad_request":               "err.bad_request",
	"internal_error":            "err.internal",
	"tfa_login_telegram_failed": "mfa.tg_failed",
	"tfa_login_email_wait":      "mfa.email_wait",
	"tfa_login_email_failed":    "mfa.email_failed",
	"invalid_request":           "err.bad_request",
	"system_error":              "err.internal",
}

func (a *App) twoFAConfirmHost() twofa.ConfirmHost[*store.User] {
	return twofa.ConfirmHost[*store.User]{
		Resolve: func(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
			u, err := a.needUser(r)
			if err != nil {
				a.fail(w, r, err)
				return nil, false
			}
			return u, true
		},
		UserID: func(u *store.User) int64 { return u.ID },
		Methods: func(u *store.User) (bool, bool, bool, error) {
			prefs, enabled, err := a.twoFAMethods(u)
			if err != nil {
				return false, false, false, err
			}
			all, tg, email := twofa.ConfirmFlags(prefs, enabled)
			return all, tg, email, nil
		},
		Texts: func(r *http.Request, u *store.User) map[string]string {
			t := func(key string) string { return a.I18n.T(a.lang(nil, r, u), key) }
			return map[string]string{
				"title":        t("mfa.tg_btn"),
				"waiting":      t("mfa.tg_waiting"),
				"denied":       t("mfa.tg_denied"),
				"expired":      t("mfa.tg_expired"),
				"failed":       t("mfa.tg_failed"),
				"cancel":       t("settings.cancel"),
				"email_title":  t("mfa.email_title"),
				"email_send":   t("mfa.email_btn"),
				"email_sent":   t("tfa_login_email_sent"),
				"email_wait":   t("mfa.email_wait"),
				"email_failed": t("mfa.email_failed"),
			}
		},
		StartTelegram: func(r *http.Request, u *store.User) (string, string, error) {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			lang := u.Locale
			return twofa.StartApproval(ctx, a.Bot, twoFATenant, u.ID, twoFAChat(u), twofa.Texts{
				Prompt:  a.tgT(lang, "tg.tfa_prompt", nil),
				Approve: a.tgT(lang, "tg.tfa_approve", nil),
				Deny:    a.tgT(lang, "tg.tfa_deny", nil),
			})
		},
		SendEmail: func(r *http.Request, u *store.User) (string, string, error) {
			return a.sendTwoFAEmailLink(r, u)
		},
		Tenant: func() string { return twoFATenant },
		Allow: func(w http.ResponseWriter, r *http.Request, u *store.User, bucket string) bool {
			prefix := map[string]string{"tg": "mfa-tg:", "mail": "mfa-mail:", "verify": "mfa:"}[bucket]
			if err := a.allow(a.mfaLimit, prefix+itoa(u.ID)); err != nil {
				a.fail(w, r, err)
				return false
			}
			return true
		},
		JSON: func(w http.ResponseWriter, r *http.Request, v any) {
			out, _ := v.(map[string]any)
			a.okJSON(w, out)
		},
		Fail: func(w http.ResponseWriter, r *http.Request, status int, key string) {
			if mapped, ok := twoFAConfirmErrKeys[key]; ok {
				key = mapped
			}
			a.fail(w, r, apiErr{status, key})
		},
	}
}

func (a *App) apiTwoFAConfirmConfig(w http.ResponseWriter, r *http.Request) {
	a.twoFAConfirmHost().Config(w, r)
}

func (a *App) apiTwoFAConfirmStart(w http.ResponseWriter, r *http.Request) {
	a.twoFAConfirmHost().Start(w, r)
}

func (a *App) apiTwoFAConfirmPoll(w http.ResponseWriter, r *http.Request) {
	a.twoFAConfirmHost().Poll(w, r)
}

func (a *App) apiTwoFAConfirmEmailSend(w http.ResponseWriter, r *http.Request) {
	a.twoFAConfirmHost().EmailSend(w, r)
}
