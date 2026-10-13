package handlers

import (
	"strconv"

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
// and takes back an earlier version when it is edited into a spoiler.
func (a *App) emitComment(u *store.User, t store.Target, parentID, commentID int64, body string, spoiler bool) {
	if a.Fed == nil {
		return
	}
	ref := "c:" + itoa(commentID)
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
