package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"tvt/auth"
	"tvt/logger"
	"tvt/store"
)

const maxPasskeys = 10

func (a *App) passkeyFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrChallengeExpired):
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.passkey_expired"})
	case errors.Is(err, auth.ErrPasskeyFailed):
		a.fail(w, r, apiErr{http.StatusUnauthorized, "err.passkey_failed"})
	case errors.Is(err, auth.ErrPasskeyUnavailable):
		a.fail(w, r, apiErr{http.StatusServiceUnavailable, "err.passkey_failed"})
	case errors.Is(err, auth.ErrDeactivated):
		a.fail(w, r, apiErr{http.StatusForbidden, "err.deactivated"})
	default:
		a.fail(w, r, err)
	}
}

func (a *App) apiPasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if err := a.allow(a.authLimit, "pk:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	options, err := a.Auth.BeginPasskeyLogin(w)
	if err != nil {
		a.passkeyFail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"options": options})
}

func (a *App) apiPasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if err := a.allow(a.authLimit, "pkf:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	u, err := a.Auth.FinishPasskeyLogin(r)
	if err != nil {
		a.passkeyFail(w, r, err)
		return
	}
	logger.Info("AUTH passkey first factor ok uid=%d", u.ID)
	a.finishLogin(w, r, u)
}

func (a *App) apiPasskeyAddBegin(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	n, err := a.Store.CountPasskeys(u.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if n >= maxPasskeys {
		a.fail(w, r, errBadRequest)
		return
	}
	options, err := a.Auth.BeginPasskeyRegistration(w, u)
	if err != nil {
		a.passkeyFail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"options": options})
}

func (a *App) apiPasskeyAddFinish(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if len([]rune(name)) > 40 {
		name = string([]rune(name)[:40])
	}
	if err := a.Auth.FinishPasskeyRegistration(r, u, name); err != nil {
		a.passkeyFail(w, r, err)
		return
	}
	logger.Info("AUTH passkey added uid=%d", u.ID)
	a.Notif.Send(u.ID, "passkey_added", "/settings?tab=account", nil)
	a.okJSON(w, nil)
}

// apiPasskeyDelete removes a passkey unless it is the account's last way to
// sign in.
func (a *App) apiPasskeyDelete(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.fail(w, r, errNotFound)
		return
	}
	n, err := a.Store.CountPasskeys(u.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	otherMethod := u.HasPassword() || (u.Email != "" && u.EmailVerified) || u.TelegramID != 0
	if n <= 1 && !otherMethod {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.passkey_last_method"})
		return
	}
	ok, err := a.Store.DeletePasskey(u.ID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !ok {
		a.fail(w, r, errNotFound)
		return
	}
	a.okJSON(w, nil)
}

func (a *App) passkeyRows(u *store.User) ([]store.PasskeyRow, error) { return a.Store.Passkeys(u.ID) }
