package handlers

import (
	"net/http"
	"strings"

	"tvt/auth"
	"tvt/logger"
	"tvt/notify"
	"tvt/store"
)

var timezones = []string{
	"UTC", "America/Los_Angeles", "America/Denver", "America/Chicago", "America/New_York", "America/Halifax", "America/Mexico_City",
	"America/Bogota", "America/Lima", "America/Sao_Paulo", "America/Argentina/Buenos_Aires", "America/Santiago", "Atlantic/Reykjavik",
	"Europe/London", "Europe/Lisbon", "Europe/Madrid", "Europe/Paris", "Europe/Berlin", "Europe/Rome", "Europe/Athens", "Europe/Istanbul",
	"Europe/Moscow", "Africa/Cairo", "Africa/Lagos", "Africa/Johannesburg", "Asia/Dubai", "Asia/Riyadh", "Asia/Tehran", "Asia/Karachi",
	"Asia/Kolkata", "Asia/Dhaka", "Asia/Bangkok", "Asia/Jakarta", "Asia/Shanghai", "Asia/Hong_Kong", "Asia/Singapore", "Asia/Seoul",
	"Asia/Tokyo", "Australia/Perth", "Australia/Sydney", "Pacific/Auckland",
}

type prefRow struct {
	ID       string
	LabelKey string
	Security bool
	Pref     store.ChannelPref
}

func (a *App) prefRows(u *store.User) ([]prefRow, error) {
	stored, err := a.Store.NotificationPrefs(u.ID)
	if err != nil {
		return nil, err
	}
	var rows []prefRow
	for _, c := range notify.Categories {
		if c.AdminOnly && !u.IsAdmin {
			continue
		}
		p, ok := stored[c.ID]
		if !ok {
			p = c.Default
		}
		if c.Security {
			p.InApp, p.Email = true, true
		}
		rows = append(rows, prefRow{ID: c.ID, LabelKey: c.LabelKey, Security: c.Security, Pref: p})
	}
	return rows, nil
}

func (a *App) pageSettings(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	tab := r.URL.Query().Get("tab")
	if tab != "profile" && tab != "notifications" && tab != "import" {
		tab = "account"
	}
	data := map[string]any{"Tab": tab, "Timezones": timezones, "Flash": r.URL.Query().Get("linked")}
	if code := r.URL.Query().Get("error"); code == "telegram_taken" || code == "internal" {
		data["FlashError"] = code
	}
	switch tab {
	case "account":
		blocked, err := a.Store.Blocked(u.ID)
		if err != nil {
			logger.Error("PAGE settings blocks uid=%d: %v", u.ID, err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
			return
		}
		left, err := a.Store.RecoveryCodesLeft(u.ID)
		if err != nil {
			logger.Error("PAGE settings recovery uid=%d: %v", u.ID, err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
			return
		}
		passkeys, err := a.passkeyRows(u)
		if err != nil {
			logger.Error("PAGE settings passkeys uid=%d: %v", u.ID, err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
			return
		}
		data["Passkeys"] = passkeys
		data["Blocked"] = blocked
		data["RecoveryLeft"] = left
	case "notifications":
		rows, err := a.prefRows(u)
		if err != nil {
			logger.Error("PAGE settings prefs uid=%d: %v", u.ID, err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
			return
		}
		data["Prefs"] = rows
		devices, err := a.Store.PushSubs(u.ID)
		if err != nil {
			logger.Error("PAGE settings push uid=%d: %v", u.ID, err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
			return
		}
		data["PushDevices"] = len(devices)
		data["TelegramReady"] = a.Bot.Configured()
		data["EmailReady"] = u.Email != "" && u.EmailVerified
	}
	a.page(w, r, "settings", "nav.settings", data)
}

func (a *App) apiUpdateProfile(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		DisplayName string `json:"display_name"`
		Bio         string
		Public      bool
		Share       bool `json:"share_activity"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.Bio = strings.TrimSpace(strings.ReplaceAll(in.Bio, "\r\n", "\n"))
	if len([]rune(in.DisplayName)) > 40 || len([]rune(in.Bio)) > 500 {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.profile_too_long"})
		return
	}
	for _, c := range in.DisplayName {
		if c < 32 {
			a.fail(w, r, errBadRequest)
			return
		}
	}
	if err := a.Store.UpdateProfile(u.ID, in.DisplayName, in.Bio, in.Public, in.Share); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}
