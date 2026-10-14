package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"tvt/federation"
	"tvt/logger"
	"tvt/store"
)

func (a *App) showOfEpisode(ep *store.Episode) *store.Show {
	sh, err := a.Store.ShowByID(ep.ShowID)
	if err != nil {
		logger.Error("FEDERATION show for episode=%d: %v", ep.ID, err)
		return nil
	}
	return sh
}

func (a *App) emitWatch(u *store.User, ep *store.Episode, count int) {
	if a.Fed == nil {
		return
	}
	sh := a.showOfEpisode(ep)
	if sh == nil {
		return
	}
	a.Fed.Emit(u, federation.Event{Ref: "w:" + itoa(ep.ID), Kind: "watch", ShowTMDB: sh.TMDBID, Show: sh.Name, Poster: sh.PosterPath,
		Season: ep.SeasonNumber, Episode: ep.EpisodeNumber, Count: count})
}

func (a *App) emitSeason(u *store.User, sh *store.Show, season, count int) {
	if a.Fed == nil || count < 1 {
		return
	}
	a.Fed.Emit(u, federation.Event{Ref: "ws:" + itoa(sh.ID) + ":" + strconv.Itoa(season), Kind: "watch", ShowTMDB: sh.TMDBID, Show: sh.Name, Poster: sh.PosterPath,
		Season: season, Count: count})
}

func (a *App) emitRating(u *store.User, ep *store.Episode, rating int) {
	if a.Fed == nil {
		return
	}
	ref := "r:" + itoa(ep.ID)
	if rating <= 0 {
		a.Fed.Retract(u, ref)
		return
	}
	sh := a.showOfEpisode(ep)
	if sh == nil {
		return
	}
	a.Fed.Emit(u, federation.Event{Ref: ref, Kind: "rating", ShowTMDB: sh.TMDBID, Show: sh.Name, Poster: sh.PosterPath,
		Season: ep.SeasonNumber, Episode: ep.EpisodeNumber, Stars: starsOf(rating)})
}

// emitComment shares an open comment on an episode, or a person's own post,
// and takes back an earlier version when it is edited into a spoiler. Episode
// comments also go, spoilers included and flagged as such, to every allow-listed
// server as leased copies.
func (a *App) emitComment(u *store.User, t store.Target, parentID, commentID int64, body string, spoiler bool) {
	if a.Fed == nil {
		return
	}
	ref := "c:" + itoa(commentID)
	if strings.TrimSpace(body) == "" {
		a.Fed.Retract(u, ref)
		a.Fed.WithdrawComment(u, ref)
		return
	}
	if t.Kind == store.KindEpisode {
		if c, err := a.Store.OpenEpisodeComment(commentID); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				logger.Error("FEDERATION share comment=%d: %v", commentID, err)
			}
		} else {
			a.Fed.ShareComment(u, federation.CommentMessage(c))
		}
	}
	if spoiler {
		a.Fed.Retract(u, ref)
		return
	}
	switch {
	case t.Kind == store.KindProfile && parentID == 0 && t.ID == u.ID:
		a.Fed.Emit(u, federation.Event{Ref: ref, Kind: "post", Excerpt: truncate(body, 400)})
	case t.Kind == store.KindEpisode:
		ep, err := a.Store.Episode(t.ID, 0)
		if err != nil {
			logger.Error("FEDERATION comment episode=%d: %v", t.ID, err)
			return
		}
		sh := a.showOfEpisode(ep)
		if sh == nil {
			return
		}
		a.Fed.Emit(u, federation.Event{Ref: ref, Kind: "comment", ShowTMDB: sh.TMDBID, Show: sh.Name, Poster: sh.PosterPath,
			Season: ep.SeasonNumber, Episode: ep.EpisodeNumber, Excerpt: truncate(body, 400)})
	}
}

// withdrawAll takes every episode comment someone has written back from the
// other servers, before they are hidden, erased or moved.
func (a *App) withdrawAll(u *store.User) {
	if a.Fed == nil {
		return
	}
	ids, err := a.Store.UserEpisodeCommentIDs(u.ID)
	if err != nil {
		logger.Error("FEDERATION withdraw comments uid=%d: %v", u.ID, err)
		return
	}
	for _, id := range ids {
		a.Fed.WithdrawComment(u, "c:"+itoa(id))
	}
}

func (a *App) apiAdminServerShare(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Host  string
		Share bool
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	host := strings.ToLower(strings.TrimSpace(in.Host))
	if err := a.Store.SetPeerShare(host, in.Share); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	logger.Info("ADMIN uid=%d set sharing with %s to %v", admin.ID, host, in.Share)
	a.okJSON(w, nil)
}

// apiReportRemoteComment reports a copy of another server's comment: it goes to
// our moderators as a report on its author and is forwarded to the comment's
// home server, whose moderators can act on the comment itself.
func (a *App) apiReportRemoteComment(w http.ResponseWriter, r *http.Request) {
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
	var in struct{ Reason string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.reportLimit, "rr:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	rc, err := a.Store.RemoteCommentByID(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	reason := truncate(strings.TrimSpace(in.Reason), 500)
	if err := a.Store.ReportUser(u.ID, rc.UserID, "comment "+rc.Ref+": "+reason); err != nil && !errors.Is(err, store.ErrAlreadyReported) {
		a.fail(w, r, err)
		return
	}
	if a.Fed != nil {
		a.Fed.ForwardReport(u, rc.Host, rc.Ref, reason)
	}
	a.okJSON(w, nil)
}
