package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tvt/auth"
	"tvt/federation"
	"tvt/logger"
	"tvt/store"
)

func (a *App) apiAdminDelete(w http.ResponseWriter, r *http.Request) {
	u, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, ok := a.commentID(w, r)
	if !ok {
		return
	}
	if err := a.Store.DeleteComment(u.ID, true, id); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	if err := a.Store.ResolveReports(id); err != nil {
		a.fail(w, r, err)
		return
	}
	logger.Info("ADMIN uid=%d deleted comment=%d", u.ID, id)
	a.okJSON(w, nil)
}

func (a *App) apiAdminResolve(w http.ResponseWriter, r *http.Request) {
	u, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, ok := a.commentID(w, r)
	if !ok {
		return
	}
	if err := a.Store.ResolveReports(id); err != nil {
		a.fail(w, r, err)
		return
	}
	logger.Info("ADMIN uid=%d resolved reports comment=%d", u.ID, id)
	a.okJSON(w, nil)
}

func (a *App) apiAdminBan(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	uid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.fail(w, r, errNotFound)
		return
	}
	var in struct{ Banned bool }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if uid == admin.ID {
		a.fail(w, r, errBadRequest)
		return
	}
	if _, err := a.Store.UserByID(uid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	if err := a.Store.SetBanned(uid, in.Banned); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.Banned {
		if err := a.Store.ResolveUserReports(uid); err != nil {
			logger.Error("ADMIN resolve user reports after ban target=%d: %v", uid, err)
		}
	}
	logger.Info("ADMIN uid=%d set banned=%v target=%d", admin.ID, in.Banned, uid)
	a.okJSON(w, nil)
}

func (a *App) logSyncFail(tmdbID int64, err error) {
	logger.Error("SYNC on-demand show tmdb=%d: %v", tmdbID, err)
}

func (a *App) apiAdminDismissUser(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	uid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.fail(w, r, errNotFound)
		return
	}
	if err := a.Store.ResolveUserReports(uid); err != nil {
		a.fail(w, r, err)
		return
	}
	logger.Info("ADMIN uid=%d dismissed user reports target=%d", admin.ID, uid)
	a.okJSON(w, nil)
}

func (a *App) apiAdminServer(forget bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admin, err := a.needAdmin(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		var in struct {
			Host    string
			Blocked bool
		}
		if err := decode(r, &in); err != nil {
			a.fail(w, r, err)
			return
		}
		host := strings.ToLower(strings.TrimSpace(in.Host))
		if host == "" {
			a.fail(w, r, errBadRequest)
			return
		}
		if forget {
			err = a.Store.ForgetPeer(host)
		} else {
			err = a.Store.SetPeerBlocked(host, in.Blocked)
		}
		switch {
		case errors.Is(err, store.ErrNotFound):
			a.fail(w, r, errNotFound)
		case err != nil:
			a.fail(w, r, err)
		default:
			logger.Info("ADMIN uid=%d server %s forget=%v blocked=%v", admin.ID, host, forget, in.Blocked)
			a.okJSON(w, nil)
		}
	}
}

const (
	adminPer       = 25
	signupsFlagKey = "signups_closed"
)

var errAdminSelf = apiErr{http.StatusBadRequest, "err.admin_self"}
var errAdminTarget = apiErr{http.StatusBadRequest, "err.admin_target"}

func (a *App) signupsClosed() bool { return a.Store.KVGet(signupsFlagKey) == "1" }

// adminBadges counts what is waiting for an admin: open reports and servers
// that connected since the Servers tab was last opened.
func (a *App) adminBadges() (reports, servers int, err error) {
	if reports, err = a.Store.OpenReportCount(); err != nil {
		return 0, 0, err
	}
	if servers, err = a.Store.UnseenPeerCount(); err != nil {
		return 0, 0, err
	}
	return reports, servers, nil
}

func (a *App) adminRedirect(tab string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin?tab="+tab, http.StatusMovedPermanently)
	}
}

type adminError struct {
	At, Level, Msg string
}

func (a *App) pageAdmin(w http.ResponseWriter, r *http.Request) {
	if _, err := a.needAdmin(r); err != nil {
		a.errorPage(w, r, http.StatusForbidden, "err.forbidden")
		return
	}
	tab := r.URL.Query().Get("tab")
	fail := func(what string, err error) {
		logger.Error("PAGE admin %s: %v", what, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
	}
	data := map[string]any{}
	switch tab {
	case "users":
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		users, total, err := a.Store.AdminUsers(q, adminPer, (page-1)*adminPer)
		if err != nil {
			fail("users", err)
			return
		}
		pages := (total + adminPer - 1) / adminPer
		if pages < 1 {
			pages = 1
		}
		data["Users"], data["Query"], data["Page"], data["Pages"], data["Total"] = users, q, page, pages, total
		data["SignupsClosed"] = a.signupsClosed()
		if page > 1 {
			data["PrevURL"] = "/admin?tab=users&page=" + strconv.Itoa(page-1) + "&q=" + url.QueryEscape(q)
		}
		if page < pages {
			data["NextURL"] = "/admin?tab=users&page=" + strconv.Itoa(page+1) + "&q=" + url.QueryEscape(q)
		}
	case "servers":
		peers, err := a.Store.Peers()
		if err != nil {
			fail("servers", err)
			return
		}
		if err := a.Store.AckPeers(); err != nil {
			logger.Error("PAGE admin servers: could not clear the new-server badge: %v", err)
		}
		data["Peers"], data["FederationOn"] = peers, a.Fed != nil
	case "reports":
		reports, err := a.Store.OpenAbuseReports(100)
		if err != nil {
			fail("reports", err)
			return
		}
		people, err := a.Store.OpenUserReports(100)
		if err != nil {
			fail("user reports", err)
			return
		}
		data["Reports"], data["PeopleReports"] = reports, people
	default:
		tab = "overview"
		st, err := a.Store.AdminStats()
		if err != nil {
			fail("overview", err)
			return
		}
		var errs []adminError
		for _, e := range logger.Recent() {
			if len(errs) == 50 {
				break
			}
			errs = append(errs, adminError{At: e.At.UTC().Format(time.RFC3339), Level: e.Level, Msg: e.Msg})
		}
		data["Stats"], data["Errors"] = st, errs
		data["Version"] = logger.Version
		data["MailOn"], data["TMDBOn"], data["FederationOn"] = a.Mail != nil && a.Mail.Configured(), a.TMDB != nil && a.TMDB.Configured(), a.Fed != nil
	}
	data["Tab"] = tab
	reports, servers, err := a.adminBadges()
	if err != nil {
		fail("badges", err)
		return
	}
	data["ReportsBadge"], data["ServersBadge"] = reports, servers
	a.page(w, r, "admin", "nav.admin", data)
}

func (a *App) apiAdminServerAdd(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Host string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if a.Fed == nil {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.federation_off"})
		return
	}
	ctx, cancel := withTimeout(r, 20*time.Second)
	defer cancel()
	host, err := a.Fed.Connect(ctx, in.Host)
	switch {
	case errors.Is(err, federation.ErrBadHost):
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.server_invalid"})
		return
	case errors.Is(err, federation.ErrUnreachable):
		logger.Warn("ADMIN uid=%d add server %q: %v", admin.ID, host, err)
		a.fail(w, r, apiErr{http.StatusBadGateway, "err.server_unreachable"})
		return
	case err != nil:
		a.fail(w, r, err)
		return
	}
	if err := a.Store.AckPeer(host); err != nil {
		logger.Error("ADMIN uid=%d add server %s: could not mark it seen: %v", admin.ID, host, err)
	}
	logger.Info("ADMIN uid=%d added server %s", admin.ID, host)
	a.okJSON(w, nil)
}

func (a *App) adminTarget(w http.ResponseWriter, r *http.Request) (admin, target *store.User, ok bool) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return nil, nil, false
	}
	uid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.fail(w, r, errNotFound)
		return nil, nil, false
	}
	target, err = a.Store.UserByID(uid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return nil, nil, false
	}
	if target.IsRemote() {
		a.fail(w, r, errNotFound)
		return nil, nil, false
	}
	return admin, target, true
}

func cleanDisplayName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	return s, len([]rune(s)) <= 40
}

func (a *App) apiAdminUserCreate(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Email, Username, Password, Display string
		Verified                           bool
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	email := ""
	if strings.TrimSpace(in.Email) != "" {
		var ok bool
		if email, ok = normEmail(in.Email); !ok {
			a.fail(w, r, apiErr{http.StatusBadRequest, "err.invalid_email"})
			return
		}
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
	display, ok := cleanDisplayName(in.Display)
	if !ok {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.profile_too_long"})
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
	u, err := a.Store.CreateUser(email, in.Username, hash, a.Cfg.DefaultLang, false)
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
	if display != "" || (email != "" && in.Verified) {
		if err := a.Store.AdminUpdateUser(u.ID, display, email, email != "" && in.Verified); err != nil {
			logger.Error("ADMIN uid=%d created user %d but could not apply display name or verification: %v", admin.ID, u.ID, err)
			a.fail(w, r, err)
			return
		}
	}
	logger.Info("ADMIN uid=%d created user uid=%d username=%s", admin.ID, u.ID, u.Username)
	a.okJSON(w, nil)
}

func (a *App) apiAdminUserUpdate(w http.ResponseWriter, r *http.Request) {
	admin, target, ok := a.adminTarget(w, r)
	if !ok {
		return
	}
	var in struct {
		Email, Display string
		Verified       bool
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	email := ""
	if strings.TrimSpace(in.Email) != "" {
		var valid bool
		if email, valid = normEmail(in.Email); !valid {
			a.fail(w, r, apiErr{http.StatusBadRequest, "err.invalid_email"})
			return
		}
	}
	display, valid := cleanDisplayName(in.Display)
	if !valid {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.profile_too_long"})
		return
	}
	verified := in.Verified && email != ""
	if err := a.Store.AdminUpdateUser(target.ID, display, email, verified); err != nil {
		if errors.Is(err, store.ErrEmailTaken) {
			a.fail(w, r, apiErr{http.StatusConflict, "err.email_taken"})
		} else {
			a.fail(w, r, err)
		}
		return
	}
	logger.Info("ADMIN uid=%d updated user uid=%d email_changed=%v verified=%v", admin.ID, target.ID, !strings.EqualFold(target.Email, email), verified)
	a.okJSON(w, nil)
}

func (a *App) apiAdminUserDelete(w http.ResponseWriter, r *http.Request) {
	admin, target, ok := a.adminTarget(w, r)
	if !ok {
		return
	}
	if target.ID == admin.ID {
		a.fail(w, r, errAdminSelf)
		return
	}
	if target.IsAdmin {
		a.fail(w, r, errAdminTarget)
		return
	}
	if err := a.deleteAccount(r.Context(), target); err != nil {
		a.fail(w, r, err)
		return
	}
	logger.Info("ADMIN uid=%d deleted user uid=%d username=%s", admin.ID, target.ID, target.Username)
	a.okJSON(w, nil)
}

func (a *App) apiAdminSignups(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Closed bool }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	v := "0"
	if in.Closed {
		v = "1"
	}
	if err := a.Store.KVSet(signupsFlagKey, v); err != nil {
		a.fail(w, r, err)
		return
	}
	logger.Info("ADMIN uid=%d set signups closed=%v", admin.ID, in.Closed)
	a.okJSON(w, nil)
}
