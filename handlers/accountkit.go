package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/CarlSinclair/csuit-libs/accountkit"
	"tvt/logger"
)

var accountKeyMap = map[string]string{
	accountkit.KeyBadPassword:   "err.bad_credentials",
	accountkit.KeyConfirmLabel:  "err.confirm_username",
	accountkit.KeyConfirmFailed: "err.internal",
	accountkit.KeySystem:        "err.internal",
	accountkit.KeyRevokeFailed:  "err.internal",
	accountkit.KeyNotSignedIn:   "err.login_required",
}

func accountKey(key string) string {
	if mapped, ok := accountKeyMap[key]; ok {
		return mapped
	}
	return key
}

func (a *App) newAccountKit() (*accountkit.Kit, error) {
	return accountkit.New(accountkit.Config{
		Identity: func(r *http.Request) (accountkit.Subject, error) {
			u, err := a.needUser(r)
			if err != nil {
				var ae apiErr
				if errors.As(err, &ae) {
					return accountkit.Subject{}, &accountkit.Error{Status: ae.status, Key: ae.key}
				}
				return accountkit.Subject{}, &accountkit.Error{Status: http.StatusInternalServerError, Key: accountkit.KeySystem, Err: err}
			}
			return accountkit.Subject{ID: u.ID, Label: u.Username, HasPassword: u.HasPassword()}, nil
		},
		CheckPassword: func(s accountkit.Subject, password string) bool {
			u, err := a.Store.UserByID(s.ID)
			if err != nil {
				logger.Error("ACCOUNT password check: load uid=%d: %v", s.ID, err)
				return false
			}
			return a.Auth.CheckPassword(u, password)
		},
		Confirm: func(r *http.Request, s accountkit.Subject) error {
			u, err := a.Store.UserByID(s.ID)
			if err != nil {
				return fmt.Errorf("load uid=%d for second factor: %w", s.ID, err)
			}
			in := accountkit.Input(r.Context())
			code := in["totp_code"]
			if code == "" {
				code = in["code"]
			}
			if err := a.requireSecondFactor(u, code); err != nil {
				var ae apiErr
				if errors.As(err, &ae) {
					return &accountkit.Error{Status: ae.status, Key: ae.key}
				}
				return err
			}
			return nil
		},
		Delete: func(ctx context.Context, s accountkit.Subject) error {
			u, err := a.Store.UserByID(s.ID)
			if err != nil {
				return err
			}
			mode, err := commentModeOf(accountkit.Input(ctx))
			if err != nil {
				return &accountkit.Error{Status: http.StatusBadRequest, Key: "err.bad_request"}
			}
			return a.deleteAccount(ctx, u, mode, false)
		},
		Deactivate: func(ctx context.Context, s accountkit.Subject) error {
			u, err := a.Store.UserByID(s.ID)
			if err != nil {
				return err
			}
			mode, err := commentModeOf(accountkit.Input(ctx))
			if err != nil {
				return &accountkit.Error{Status: http.StatusBadRequest, Key: "err.bad_request"}
			}
			a.withdrawAll(u)
			if err := a.applyCommentMode(u.ID, mode); err != nil {
				return err
			}
			return a.Store.DeactivateSelf(u.ID)
		},
		RevokeAll: func(r *http.Request, s accountkit.Subject, keepCurrent bool) error {
			return a.Auth.RevokeAll(s.ID)
		},
		ClearSession: func(w http.ResponseWriter) { a.Auth.ClearCookies(w) },
		Export: func(ctx context.Context, s accountkit.Subject) (map[string]any, error) {
			return a.Store.ExportUser(s.ID)
		},
		Audit: func(r *http.Request, s accountkit.Subject, action string) {
			switch action {
			case "account_deleted":
				a.audit(r, "ACCOUNT deleted uid=%d username=%s", s.ID, s.Label)
			case "account_deactivated":
				a.audit(r, "ACCOUNT deactivated uid=%d", s.ID)
			case "sessions_revoked":
				a.audit(r, "ACCOUNT sessions_revoked uid=%d", s.ID)
				a.Notif.Send(s.ID, "session_revoked", "/settings?tab=account", nil)
			case "account_exported":
				a.audit(r, "ACCOUNT exported uid=%d", s.ID)
			default:
				a.audit(r, "ACCOUNT %s uid=%d", action, s.ID)
			}
		},
		WriteError: func(w http.ResponseWriter, r *http.Request, status int, key string) {
			a.fail(w, r, apiErr{status, accountKey(key)})
		},
		WriteOK: func(w http.ResponseWriter, r *http.Request, redirect string) {
			a.okJSON(w, map[string]any{"redirect": redirect})
		},
		AccountURL: "/login",
		HomeURL:    "/",
	})
}

func commentModeOf(in map[string]string) (string, error) {
	t := commentToggles{HideComments: in["hide_comments"] == "true", DeleteComments: in["delete_comments"] == "true"}
	return t.mode()
}
