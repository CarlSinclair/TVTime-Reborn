package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tvt/auth"
	"tvt/i18n"
	"tvt/logger"
	"tvt/store"
	"tvt/telegram"

	"github.com/CarlSinclair/csuit-libs/twofa"
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
	raw, err := a.Store.TwoFAPrefs(u.ID)
	if err != nil {
		return twofa.Prefs{}, nil, err
	}
	p := twofa.ParsePrefs(raw)
	if p.Email && (strings.TrimSpace(u.Email) == "" || !a.mailOK()) {
		p.Email = false
	}
	return p, p.Enabled(u.TOTPEnabled, p.Telegram && twoFAChat(u) != 0 && a.Bot.Configured()), nil
}

func (a *App) twoFARequired(u *store.User, via twofa.Via) (bool, error) {
	p, enabled, err := a.twoFAMethods(u)
	if err != nil {
		return false, err
	}
	return p.NeedsStep(via, enabled), nil
}

func (a *App) sendTwoFAEmail(u *store.User) error {
	if strings.TrimSpace(u.Email) == "" || !a.mailOK() {
		return errMailDown
	}
	code, err := twofa.NewEmailCode(twoFATenant, u.ID)
	if err != nil {
		if errors.Is(err, twofa.ErrEmailCooldown) {
			return apiErr{http.StatusTooManyRequests, "mfa.email_wait"}
		}
		return err
	}
	lang := u.Locale
	if !a.I18n.Has(lang) {
		lang = i18n.Fallback
	}
	if err := a.Mail.Send(u.Email, a.I18n.T(lang, "mail.tfa.subject", a.Cfg.AppName), a.I18n.T(lang, "mail.tfa.body", a.Cfg.AppName, code)); err != nil {
		twofa.DiscardEmailCode(twoFATenant, u.ID)
		logger.Error("AUTH 2fa_email send uid=%d: %v", u.ID, err)
		return errMailDown
	}
	return nil
}

func (a *App) checkTwoFAEmail(u *store.User, code string) (bool, error) {
	return twofa.CheckEmailCode(twoFATenant, u.ID, strings.TrimSpace(code))
}

type twoFAStep struct {
	user    *store.User
	prefs   twofa.Prefs
	enabled []twofa.Method
	done    map[twofa.Method]bool
	token   string
}

func (s *twoFAStep) has(m twofa.Method) bool {
	for _, e := range s.enabled {
		if e == m {
			return true
		}
	}
	return false
}

func (a *App) loadTwoFAStep(r *http.Request) (*twoFAStep, error) {
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
	prefs, enabled, err := a.twoFAMethods(u)
	if err != nil {
		logger.Error("AUTH 2fa_step prefs uid=%d: %v", uid, err)
		return nil, err
	}
	done, err := twofa.Done(token)
	if err != nil {
		logger.Error("AUTH 2fa_step progress read uid=%d: %v", uid, err)
		return nil, err
	}
	return &twoFAStep{user: u, prefs: prefs, enabled: enabled, done: done, token: token}, nil
}

func (a *App) finishTwoFAMethod(w http.ResponseWriter, s *twoFAStep, m twofa.Method) (bool, error) {
	if err := twofa.MarkDone(s.token, m); err != nil {
		logger.Error("AUTH 2fa_step could not record method=%s uid=%d: %v", m, s.user.ID, err)
		return false, err
	}
	s.done[m] = true
	if !s.prefs.Satisfied(s.enabled, s.done) {
		return false, nil
	}
	twofa.ClearDone(s.token)
	a.Auth.ClearMFA(w)
	return true, nil
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
		"TOTP":     s.has(twofa.MethodTOTP) && !s.done[twofa.MethodTOTP],
		"Telegram": s.has(twofa.MethodTelegram) && !s.done[twofa.MethodTelegram],
		"Email":    s.has(twofa.MethodEmail) && !s.done[twofa.MethodEmail],
	})
}

func (a *App) apiTwoFAEmailSend(w http.ResponseWriter, r *http.Request) {
	s, err := a.loadTwoFAStep(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !s.has(twofa.MethodEmail) || s.done[twofa.MethodEmail] {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.allow(a.mfaLimit, "mfa-mail:"+itoa(s.user.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.sendTwoFAEmail(s.user); err != nil {
		a.fail(w, r, err)
		return
	}
	logger.Info("AUTH 2fa_email sent uid=%d from=%s", s.user.ID, auth.ClientCountry(r))
	a.okJSON(w, nil)
}

func (a *App) apiTwoFAEmailVerify(w http.ResponseWriter, r *http.Request) {
	var in struct{ Code string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	s, err := a.loadTwoFAStep(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !s.has(twofa.MethodEmail) {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.allow(a.mfaLimit, "mfa:"+itoa(s.user.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	ok, err := a.checkTwoFAEmail(s.user, in.Code)
	if err != nil {
		logger.Error("AUTH 2fa_email check uid=%d: %v", s.user.ID, err)
		a.fail(w, r, err)
		return
	}
	if !ok {
		logger.Warn("AUTH 2fa_email bad_code uid=%d from=%s", s.user.ID, auth.ClientCountry(r))
		a.fail(w, r, apiErr{http.StatusUnauthorized, "err.mfa_bad_code"})
		return
	}
	complete, err := a.finishTwoFAMethod(w, s, twofa.MethodEmail)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !complete {
		a.okJSON(w, map[string]any{"redirect": "/login/2fa"})
		return
	}
	if err := a.Auth.StartSessionFor(w, r, s.user.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	logger.Info("AUTH 2fa_ok uid=%d method=email from=%s", s.user.ID, auth.ClientCountry(r))
	a.okJSON(w, map[string]any{"redirect": "/"})
}

func (a *App) apiTwoFATelegramStart(w http.ResponseWriter, r *http.Request) {
	s, err := a.loadTwoFAStep(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !s.has(twofa.MethodTelegram) {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.allow(a.mfaLimit, "mfa-tg:"+itoa(s.user.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	lang := s.user.Locale
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token, secret, err := twofa.StartApproval(ctx, a.Bot, twoFATenant, s.user.ID, twoFAChat(s.user), twofa.Texts{
		Prompt:  a.tgT(lang, "tg.tfa_prompt", nil),
		Approve: a.tgT(lang, "tg.tfa_approve", nil),
		Deny:    a.tgT(lang, "tg.tfa_deny", nil),
	})
	if err != nil {
		logger.Error("AUTH 2fa_telegram start failed uid=%d: %v", s.user.ID, err)
		a.fail(w, r, apiErr{http.StatusBadGateway, "mfa.tg_failed"})
		return
	}
	logger.Info("AUTH 2fa_telegram sent uid=%d from=%s", s.user.ID, auth.ClientCountry(r))
	w.Header().Set("Cache-Control", "no-store")
	a.okJSON(w, map[string]any{"token": token, "secret": secret})
}

func (a *App) apiTwoFATelegramPoll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s, err := a.loadTwoFAStep(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status, uid := twofa.PollApproval(r.URL.Query().Get("t"), r.Header.Get("X-Login-Secret"))
	if status != "approved" {
		a.okJSON(w, map[string]any{"status": status})
		return
	}
	if uid != s.user.ID {
		logger.Warn("AUTH 2fa_telegram approval for another account uid=%d approved_uid=%d", s.user.ID, uid)
		a.okJSON(w, map[string]any{"status": "expired"})
		return
	}
	complete, err := a.finishTwoFAMethod(w, s, twofa.MethodTelegram)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !complete {
		a.okJSON(w, map[string]any{"status": "more"})
		return
	}
	if err := a.Auth.StartSessionFor(w, r, s.user.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	logger.Info("AUTH 2fa_ok uid=%d method=telegram from=%s", s.user.ID, auth.ClientCountry(r))
	a.okJSON(w, map[string]any{"status": "approved", "next": "/"})
}

func (a *App) apiTwoFAPrefs(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Password      string
		Code          string
		Telegram      bool
		Email         bool
		Mode          string
		PasskeySkips  bool `json:"passkey_skips"`
		MagicSkips    bool `json:"magic_skips"`
		TelegramSkips bool `json:"telegram_skips"`
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
	p := twofa.Prefs{Telegram: in.Telegram, Email: in.Email, All: in.Mode == "all", PasskeySkips: in.PasskeySkips, MagicSkips: in.MagicSkips, TelegramSkips: in.TelegramSkips}
	if p.Email && (strings.TrimSpace(u.Email) == "" || !a.mailOK()) {
		a.fail(w, r, apiErr{http.StatusBadRequest, "settings.tfa_email_needs_mail"})
		return
	}
	if p.Telegram && (twoFAChat(u) == 0 || !a.Bot.Configured()) {
		a.fail(w, r, apiErr{http.StatusBadRequest, "settings.tfa_telegram_needs_link"})
		return
	}
	if err := a.Store.SetTwoFAPrefs(u.ID, p.Encode()); err != nil {
		logger.Error("AUTH 2fa prefs save uid=%d: %v", u.ID, err)
		a.fail(w, r, apiErr{http.StatusInternalServerError, "err.tfa_prefs_failed"})
		return
	}
	logger.Info("AUTH 2fa prefs saved uid=%d prefs=%s", u.ID, p.Encode())
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

func (a *App) apiTwoFAConfirmConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	prefs, enabled, err := a.twoFAMethods(u)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	telegram, email := false, false
	for _, m := range enabled {
		switch m {
		case twofa.MethodTelegram:
			telegram = true
		case twofa.MethodEmail:
			email = true
		}
	}
	t := func(key string) string { return a.I18n.T(a.lang(w, r, u), key) }
	a.okJSON(w, map[string]any{
		"telegram": telegram,
		"email":    email,
		"all":      prefs.All,
		"t": map[string]string{
			"title":        t("mfa.tg_btn"),
			"waiting":      t("mfa.tg_waiting"),
			"denied":       t("mfa.tg_denied"),
			"expired":      t("mfa.tg_expired"),
			"failed":       t("mfa.tg_failed"),
			"cancel":       t("settings.cancel"),
			"email_title":  t("mfa.email_title"),
			"email_send":   t("mfa.email_btn"),
			"email_sent":   t("mfa.email_sent"),
			"email_wait":   t("mfa.email_wait"),
			"email_failed": t("mfa.email_failed"),
			"email_bad":    t("err.mfa_bad_code"),
			"email_label":  t("mfa.email_label"),
			"email_verify": t("token.continue"),
		},
	})
}

func (a *App) apiTwoFAConfirmStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	_, enabled, err := a.twoFAMethods(u)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	telegram := false
	for _, m := range enabled {
		if m == twofa.MethodTelegram {
			telegram = true
		}
	}
	if !telegram {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.allow(a.mfaLimit, "mfa-tg:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	lang := u.Locale
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token, secret, err := twofa.StartApproval(ctx, a.Bot, twoFATenant, u.ID, twoFAChat(u), twofa.Texts{
		Prompt:  a.tgT(lang, "tg.tfa_prompt", nil),
		Approve: a.tgT(lang, "tg.tfa_approve", nil),
		Deny:    a.tgT(lang, "tg.tfa_deny", nil),
	})
	if err != nil {
		logger.Error("AUTH 2fa_confirm start failed uid=%d: %v", u.ID, err)
		a.fail(w, r, apiErr{http.StatusBadGateway, "mfa.tg_failed"})
		return
	}
	logger.Info("AUTH 2fa_confirm sent uid=%d", u.ID)
	a.okJSON(w, map[string]any{"token": token, "secret": secret})
}

func (a *App) apiTwoFAConfirmPoll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status, uid := twofa.PollApproval(r.URL.Query().Get("t"), r.Header.Get("X-Login-Secret"))
	if status != "approved" {
		a.okJSON(w, map[string]any{"status": status})
		return
	}
	if uid != u.ID {
		logger.Warn("AUTH 2fa_confirm approval for another account uid=%d approved_uid=%d", u.ID, uid)
		a.okJSON(w, map[string]any{"status": "expired"})
		return
	}
	if err := twofa.MarkReauth(twoFATenant, u.ID, twofa.MethodTelegram); err != nil {
		logger.Error("AUTH 2fa_confirm could not record approval uid=%d: %v", u.ID, err)
		a.fail(w, r, apiErr{http.StatusInternalServerError, "mfa.tg_failed"})
		return
	}
	a.okJSON(w, map[string]any{"status": "approved"})
}

func (a *App) apiTwoFAConfirmEmailSend(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	_, enabled, err := a.twoFAMethods(u)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	on := false
	for _, m := range enabled {
		if m == twofa.MethodEmail {
			on = true
		}
	}
	if !on {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.allow(a.mfaLimit, "mfa-mail:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.sendTwoFAEmail(u); err != nil {
		a.fail(w, r, err)
		return
	}
	logger.Info("AUTH 2fa_confirm email sent uid=%d", u.ID)
	a.okJSON(w, nil)
}

func (a *App) apiTwoFAConfirmEmailVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.mfaLimit, "mfa:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	ok, err := a.checkTwoFAEmail(u, r.Header.Get("X-Email-Code"))
	if err != nil {
		logger.Error("AUTH 2fa_confirm email check uid=%d: %v", u.ID, err)
		a.fail(w, r, err)
		return
	}
	if !ok {
		logger.Warn("AUTH 2fa_confirm email bad_code uid=%d", u.ID)
		a.fail(w, r, apiErr{http.StatusUnauthorized, "err.mfa_bad_code"})
		return
	}
	if err := twofa.MarkReauth(twoFATenant, u.ID, twofa.MethodEmail); err != nil {
		logger.Error("AUTH 2fa_confirm could not record email proof uid=%d: %v", u.ID, err)
		a.fail(w, r, apiErr{http.StatusInternalServerError, "mfa.email_failed"})
		return
	}
	a.okJSON(w, map[string]any{"status": "approved"})
}
