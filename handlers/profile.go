package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"tvt/auth"
	"tvt/logger"
	"tvt/store"
)

func (a *App) pageOwnProfile(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/u/"+u.Username, http.StatusSeeOther)
}

func (a *App) pageProfile(w http.ResponseWriter, r *http.Request) {
	viewer := auth.UserFrom(r.Context())
	lang := a.langOf(w, r)
	p, err := a.Store.UserByUsername(r.PathValue("username"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			if cur, ok, rerr := a.Store.UsernameRedirect(r.PathValue("username")); rerr != nil {
				logger.Error("PAGE profile redirect username=%q: %v", r.PathValue("username"), rerr)
			} else if ok {
				http.Redirect(w, r, "/u/"+cur, http.StatusMovedPermanently)
				return
			}
			a.errorPage(w, r, http.StatusNotFound, "err.not_found")
		} else {
			logger.Error("PAGE profile username=%q: %v", r.PathValue("username"), err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		}
		return
	}
	if p.IsDeactivated && (viewer == nil || !viewer.IsAdmin) {
		a.errorPage(w, r, http.StatusNotFound, "err.not_found")
		return
	}
	isOwn := viewer != nil && viewer.ID == p.ID
	data := map[string]any{"Profile": p, "IsOwn": isOwn, "ProfileURL": a.Cfg.BaseURL + "/u/" + p.Username}
	if bu, perr := url.Parse(a.Cfg.BaseURL); perr == nil && !p.IsRemote() {
		data["Handle"] = p.Username + "@" + bu.Host
	}
	if p.IsRemote() {
		data["Remote"] = true
		data["Handle"] = p.Username
		scheme := "https"
		if a.Cfg.FedInsecure {
			scheme = "http"
		}
		data["RemoteURL"] = scheme + "://" + p.RemoteHost + "/u/" + p.LocalPart()
		if viewer != nil {
			blocked, err := a.Store.BlockedEither(viewer.ID, p.ID)
			if err != nil {
				logger.Error("PAGE profile block check: %v", err)
				a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
				return
			}
			if blocked {
				a.errorPage(w, r, http.StatusNotFound, "err.not_found")
				return
			}
			state, err := a.Store.FriendState(viewer.ID, p.ID)
			if err != nil {
				logger.Error("PAGE profile friend state: %v", err)
				a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
				return
			}
			data["Friend"] = state
		}
		a.render(w, r, "profile", http.StatusOK, p.Name(), data)
		return
	}
	if viewer != nil && !isOwn {
		blocked, err := a.Store.BlockedEither(viewer.ID, p.ID)
		if err != nil {
			logger.Error("PAGE profile block check: %v", err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
			return
		}
		if blocked {
			a.errorPage(w, r, http.StatusNotFound, "err.not_found")
			return
		}
	}
	if _, err := a.canSeeProfileThread(viewer, p.ID); err == nil {
		data["Posts"] = true
	}
	if viewer != nil && !isOwn {
		state, err := a.Store.FriendState(viewer.ID, p.ID)
		if err != nil {
			logger.Error("PAGE profile friend state: %v", err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
			return
		}
		data["Friend"] = state
	}
	if !p.ProfilePublic && !isOwn && (viewer == nil || !viewer.IsAdmin) {
		data["Private"] = true
		a.render(w, r, "profile", http.StatusOK, p.Name(), data)
		return
	}
	stats, err := a.Store.WatchStats(p.ID, p.Location(), time.Now())
	if err != nil {
		logger.Error("PAGE profile stats uid=%d: %v", p.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	genres, err := a.Store.FavoriteGenres(p.ID, 5)
	if err != nil {
		logger.Error("PAGE profile genres uid=%d: %v", p.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	favTotal, err := a.Store.CountFavoriteShows(p.ID)
	if err != nil {
		logger.Error("PAGE profile favorite count uid=%d: %v", p.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	favOffset, favPager := paginate(r, "fav", 12, favTotal, "favorites")
	favShows, err := a.Store.FavoriteShows(p.ID, 12, favOffset)
	if err != nil {
		logger.Error("PAGE profile favorite shows uid=%d: %v", p.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	a.localizeShowList(lang, favShows)
	recentTotal, err := a.Store.CountWatched(p.ID)
	if err != nil {
		logger.Error("PAGE profile watched count uid=%d: %v", p.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	recentOffset, recentPager := paginate(r, "recent", 8, recentTotal, "recent")
	recent, err := a.Store.RecentlyWatched(p.ID, 8, recentOffset)
	if err != nil {
		logger.Error("PAGE profile recent uid=%d: %v", p.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	a.localizeCalendar(lang, recent)
	data["Stats"], data["Genres"], data["FavShows"], data["Recent"] = stats, genres, favShows, recent
	data["FavTotal"], data["FavPager"], data["RecentTotal"], data["RecentPager"] = favTotal, favPager, recentTotal, recentPager
	a.render(w, r, "profile", http.StatusOK, p.Name(), data)
}
