package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"tvt/store"
	"tvt/tmdb"
)

func (a *App) showForAPI(w http.ResponseWriter, r *http.Request) (*store.Show, bool) {
	tid, err := strconv.ParseInt(r.PathValue("tmdb"), 10, 64)
	if err != nil || tid <= 0 {
		a.fail(w, r, errNotFound)
		return nil, false
	}
	ctx, cancel := withTimeout(r, 45*time.Second)
	defer cancel()
	sh, err := a.Sync.EnsureShow(ctx, tid)
	switch {
	case err == nil:
		return sh, true
	case errors.Is(err, tmdb.ErrNotFound):
		a.fail(w, r, errNotFound)
	case errors.Is(err, tmdb.ErrNoKey):
		a.fail(w, r, errTMDBMissing)
	default:
		a.fail(w, r, errTMDBDown)
		a.logSyncFail(tid, err)
	}
	return nil, false
}

func (a *App) apiFollow(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Status string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.Status == "" {
		in.Status = "watching"
	}
	if !store.ValidStatus(in.Status) {
		a.fail(w, r, errBadRequest)
		return
	}
	sh, ok := a.showForAPI(w, r)
	if !ok {
		return
	}
	if err := a.Store.Follow(u.ID, sh.ID, in.Status); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"status": in.Status})
}

func (a *App) apiUnfollow(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sh, ok := a.showForAPI(w, r)
	if !ok {
		return
	}
	if err := a.Store.Unfollow(u.ID, sh.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

func (a *App) apiFavorite(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Favorite bool }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	sh, ok := a.showForAPI(w, r)
	if !ok {
		return
	}
	if err := a.Store.SetFavorite(u.ID, sh.ID, in.Favorite); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errBadRequest)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	a.okJSON(w, nil)
}

func (a *App) ensureFollowing(userID, showID int64) error {
	f, err := a.Store.FollowOf(userID, showID)
	if err != nil {
		return err
	}
	if f == nil {
		return a.Store.Follow(userID, showID, "watching")
	}
	return nil
}

func (a *App) apiWatch(w http.ResponseWriter, r *http.Request) {
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
	var in struct{ Watched, UpTo bool }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	ep, err := a.Store.Episode(id, 0)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	marked := 0
	switch {
	case in.UpTo:
		marked, err = a.Store.MarkUpTo(u.ID, ep.ShowID, ep.SeasonNumber, ep.EpisodeNumber)
		if err == nil {
			err = a.Store.MarkWatched(u.ID, ep.ID, "")
		}
	case in.Watched:
		err = a.Store.MarkWatched(u.ID, ep.ID, "")
		marked = 1
	default:
		err = a.Store.Unwatch(u.ID, ep.ID)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if in.Watched || in.UpTo {
		if err := a.ensureFollowing(u.ID, ep.ShowID); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	p, err := a.Store.Progress(u.ID, ep.ShowID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"marked": marked, "watched": p.Watched, "aired": p.Aired})
}

func (a *App) apiWatchSeason(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 {
		a.fail(w, r, errNotFound)
		return
	}
	var in struct{ Watched bool }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	sh, ok := a.showForAPI(w, r)
	if !ok {
		return
	}
	marked, err := a.Store.MarkSeason(u.ID, sh.ID, n, in.Watched)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if in.Watched {
		if err := a.ensureFollowing(u.ID, sh.ID); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	a.okJSON(w, map[string]any{"marked": marked})
}

func (a *App) apiRate(w http.ResponseWriter, r *http.Request) {
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
	var in struct{ Rating int }
	if err := decode(r, &in); err != nil || in.Rating < 0 || in.Rating > 10 {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.Store.Rate(u.ID, id, in.Rating); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	a.okJSON(w, nil)
}
