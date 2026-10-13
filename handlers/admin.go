package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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

func (a *App) pageAdminServers(w http.ResponseWriter, r *http.Request) {
	if _, err := a.needAdmin(r); err != nil {
		a.errorPage(w, r, http.StatusForbidden, "err.forbidden")
		return
	}
	peers, err := a.Store.Peers()
	if err != nil {
		logger.Error("PAGE admin servers: %v", err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	a.page(w, r, "admin_servers", "admin.servers", map[string]any{"Peers": peers})
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
