package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tvt/logger"
	"tvt/store"
	"tvt/tmdb"
	"tvt/tvdb"
)

func (a *App) showForAPI(w http.ResponseWriter, r *http.Request) (*store.Show, bool) {
	tid, err := strconv.ParseInt(r.PathValue("tmdb"), 10, 64)
	if err != nil || tid == 0 {
		a.fail(w, r, errNotFound)
		return nil, false
	}
	ctx, cancel := withTimeout(r, 45*time.Second)
	defer cancel()
	sh, err := a.Sync.EnsureShow(ctx, tid)
	switch {
	case err == nil:
		return sh, true
	case errors.Is(err, tmdb.ErrNotFound), errors.Is(err, tvdb.ErrNotFound):
		a.fail(w, r, errNotFound)
	case errors.Is(err, tmdb.ErrNoKey), errors.Is(err, tvdb.ErrNoKey):
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
		if err := a.Store.Follow(userID, showID, "watching"); err != nil {
			return err
		}
	}
	if _, err := a.Store.CompleteFinished(userID, showID); err != nil {
		logger.Error("COMPLETE after watch uid=%d show=%d: %v", userID, showID, err)
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
	var in struct{ Watched, UpTo, Card bool }
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
	if in.Watched || in.UpTo {
		n := 1
		if in.UpTo && marked > 1 {
			n = marked
		}
		a.emitWatch(u, ep, n)
	}
	out := map[string]any{"marked": marked, "watched": p.Watched, "aired": p.Aired}
	if in.Card {
		out["next_html"] = a.upNextHTML(w, r, u, ep.ShowID)
	}
	a.okJSON(w, out)
}

// upNextHTML renders the show's card for the home page, or "" when the person
// has nothing left to watch of it, so the page can swap it in place.
func (a *App) upNextHTML(w http.ResponseWriter, r *http.Request, u *store.User, showID int64) string {
	it, err := a.Store.UpNextFor(u.ID, showID)
	if err != nil {
		logger.Error("API up next card uid=%d show=%d: %v", u.ID, showID, err)
		return ""
	}
	if it == nil {
		return ""
	}
	lang := a.lang(w, r, u)
	items := []store.UpNextItem{*it}
	a.localizeUpNext(lang, items)
	var buf strings.Builder
	v := &View{App: a, Lang: lang, User: u}
	if err := a.tmpl["home"].ExecuteTemplate(&buf, "upnext_card", map[string]any{"It": items[0], "V": v}); err != nil {
		logger.Error("API up next card render show=%d: %v", showID, err)
		return ""
	}
	return buf.String()
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
	if in.Watched {
		a.emitSeason(u, sh, n, marked)
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
	var in struct {
		Rating int
		Card   bool
	}
	if err := decode(r, &in); err != nil || in.Rating < 0 || in.Rating > 10 {
		a.fail(w, r, errBadRequest)
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
	if err := a.Store.Rate(u.ID, id, in.Rating); err != nil {
		a.fail(w, r, err)
		return
	}
	out := map[string]any{"watched": false}
	if in.Rating > 0 && aired(ep.AirDate) {
		if err := a.Store.MarkWatched(u.ID, ep.ID, ""); err != nil {
			a.fail(w, r, err)
			return
		}
		if err := a.ensureFollowing(u.ID, ep.ShowID); err != nil {
			a.fail(w, r, err)
			return
		}
		out["watched"] = true
		a.emitWatch(u, ep, 1)
	}
	a.emitRating(u, ep, in.Rating)
	if in.Card {
		out["next_html"] = a.upNextHTML(w, r, u, ep.ShowID)
	}
	a.okJSON(w, out)
}
