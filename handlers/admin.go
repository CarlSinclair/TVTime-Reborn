package handlers

import (
	"errors"
	"net/http"
	"strconv"

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
	logger.Info("ADMIN uid=%d set banned=%v target=%d", admin.ID, in.Banned, uid)
	a.okJSON(w, nil)
}

func (a *App) logSyncFail(tmdbID int64, err error) {
	logger.Error("SYNC on-demand show tmdb=%d: %v", tmdbID, err)
}
