package handlers

import (
	"encoding/base64"
	"errors"
	"net/http"

	"tvt/auth"
	"tvt/logger"
	"tvt/store"
)

// finishLogin is the single exit for every successful first factor (password,
// magic link, Telegram): accounts with 2FA get a short-lived pending cookie and
// must pass the second-factor step before any session exists.
func (a *App) finishLogin(w http.ResponseWriter, r *http.Request, u *store.User) {
	if u.TOTPEnabled {
		if err := a.Auth.BeginMFA(w, u.ID); err != nil {
			a.fail(w, r, err)
			return
		}
		a.okJSON(w, map[string]any{"mfa": true, "redirect": "/login/2fa"})
		return
	}
	if err := a.Auth.StartSession(w, u.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"redirect": "/"})
}

func (a *App) finishLoginRedirect(w http.ResponseWriter, r *http.Request, u *store.User) {
	if u.TOTPEnabled {
		if err := a.Auth.BeginMFA(w, u.ID); err != nil {
			logger.Error("AUTH begin mfa uid=%d: %v", u.ID, err)
			http.Redirect(w, r, "/login?error=internal", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/login/2fa", http.StatusSeeOther)
		return
	}
	if err := a.Auth.StartSession(w, u.ID); err != nil {
		logger.Error("AUTH start session uid=%d: %v", u.ID, err)
		http.Redirect(w, r, "/login?error=internal", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) pageMFA(w http.ResponseWriter, r *http.Request) {
	if _, err := a.Auth.PendingMFAUser(r); err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	a.page(w, r, "mfa", "mfa.title", nil)
}

func (a *App) apiMFAVerify(w http.ResponseWriter, r *http.Request) {
	var in struct{ Code string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	uid, err := a.Auth.PendingMFAUser(r)
	if err != nil {
		a.fail(w, r, apiErr{http.StatusUnauthorized, "err.mfa_expired"})
		return
	}
	if err := a.allow(a.mfaLimit, "mfa:"+itoa(uid)); err != nil {
		a.fail(w, r, err)
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
	if err := a.Auth.VerifySecondFactor(u, in.Code); err != nil {
		if errors.Is(err, auth.ErrBadMFACode) {
			logger.Warn("AUTH 2fa failed uid=%d from=%s", uid, auth.ClientCountry(r))
			a.fail(w, r, apiErr{http.StatusUnauthorized, "err.mfa_bad_code"})
		} else {
			a.fail(w, r, err)
		}
		return
	}
	a.Auth.ClearMFA(w)
	if err := a.Auth.StartSession(w, uid); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"redirect": "/"})
}

func (a *App) apiTOTPSetup(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if u.TOTPEnabled {
		a.fail(w, r, errBadRequest)
		return
	}
	secret, uri, qr, err := a.Auth.BeginTOTPSetup(u, a.Cfg.AppName)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"secret": secret, "uri": uri, "qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(qr)})
}

func (a *App) apiTOTPEnable(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Code string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.mfaLimit, "mfa-setup:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	codes, err := a.Auth.EnableTOTP(u, in.Code)
	if err != nil {
		if errors.Is(err, auth.ErrBadMFACode) {
			a.fail(w, r, apiErr{http.StatusBadRequest, "err.mfa_bad_code"})
		} else {
			a.fail(w, r, err)
		}
		return
	}
	logger.Info("AUTH 2fa enabled uid=%d", u.ID)
	a.Notif.Send(u.ID, "twofa_enabled", "/settings?tab=account", nil)
	a.okJSON(w, map[string]any{"recovery_codes": codes})
}

// requireReauth checks the account password (or, for password-less accounts,
// a current second-factor code) before a sensitive change.
func (a *App) requireReauth(u *store.User, password, code string) error {
	if u.HasPassword() {
		if !a.Auth.CheckPassword(u, password) {
			return apiErr{http.StatusUnauthorized, "err.bad_credentials"}
		}
		return nil
	}
	if u.TOTPEnabled {
		if err := a.Auth.VerifySecondFactor(u, code); err != nil {
			return apiErr{http.StatusUnauthorized, "err.mfa_bad_code"}
		}
		return nil
	}
	return nil
}

func (a *App) apiTOTPDisable(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Password, Code string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if !u.TOTPEnabled {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.allow(a.mfaLimit, "mfa-off:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	if u.HasPassword() {
		if err := a.requireReauth(u, in.Password, ""); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	if err := a.Auth.VerifySecondFactor(u, in.Code); err != nil {
		a.fail(w, r, apiErr{http.StatusUnauthorized, "err.mfa_bad_code"})
		return
	}
	if err := a.Auth.DisableTOTP(u.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	logger.Info("AUTH 2fa disabled uid=%d", u.ID)
	a.Notif.Send(u.ID, "twofa_disabled", "/settings?tab=account", nil)
	a.okJSON(w, nil)
}

func (a *App) apiRecoveryRegenerate(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Code string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if !u.TOTPEnabled {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.allow(a.mfaLimit, "mfa-regen:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Auth.VerifySecondFactor(u, in.Code); err != nil {
		a.fail(w, r, apiErr{http.StatusUnauthorized, "err.mfa_bad_code"})
		return
	}
	fresh, err := a.Store.UserByID(u.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	codes, err := a.Auth.RegenerateRecoveryCodes(fresh)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"recovery_codes": codes})
}

func (a *App) apiLogoutEverywhere(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Auth.RevokeAll(u.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.Auth.ClearCookies(w)
	a.okJSON(w, map[string]any{"redirect": "/login"})
}
