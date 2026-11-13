package federation

import (
	"tvt/logger"
	"tvt/youtube"
)

// addYouTubeRefs names a YouTube show and episode by their channel or playlist
// and video, which every server shares, next to the local catalog numbers that
// only mean something here.
func (s *Service) addYouTubeRefs(ev *Event) {
	if ev.ShowTMDB >= 0 {
		return
	}
	src, vid, err := s.Store.YouTubeRefs(ev.ShowTMDB, ev.Season, ev.Episode)
	if err != nil {
		logger.Error("FEDERATION youtube refs show=%d s%de%d: %v", ev.ShowTMDB, ev.Season, ev.Episode, err)
		return
	}
	ev.YT = src
	if ev.Episode > 0 {
		ev.YTV = vid
	}
}

// localYouTube turns a peer's reference to a YouTube show or episode into this
// server's catalog id and season and episode numbers, creating the show entry
// the first time the source is seen. Without a source the numbers pass through
// unchanged. found is false when the message names a video this server does
// not have, so it cannot be put on the right episode.
func (s *Service) localYouTube(source, video string, showTMDB int64, name, poster string, season, episode int) (int64, int, int, bool, error) {
	if source == "" {
		return showTMDB, season, episode, true, nil
	}
	if !youtube.ValidSourceID(source) || (video != "" && !youtube.ValidVideoID(video)) {
		return 0, 0, 0, false, errBadMessage
	}
	id, err := s.Store.ResolveYouTubeSource(source, showTMDB, clip(name, 200), sanitizePoster(poster))
	if err != nil {
		return 0, 0, 0, false, err
	}
	if video == "" {
		return id, season, episode, true, nil
	}
	sn, ep, ok, err := s.Store.YouTubeEpisode(id, video)
	if err != nil {
		return 0, 0, 0, false, err
	}
	if !ok {
		logger.Info("FEDERATION youtube video %s of source %s is not on this server yet, message dropped", video, source)
		return 0, 0, 0, false, nil
	}
	return id, sn, ep, true, nil
}
