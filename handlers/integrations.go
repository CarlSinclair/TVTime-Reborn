package handlers

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tvt/auth"
	"tvt/logger"
	"tvt/store"
)

const (
	maxSyncVideoIDs   = 500
	maxScrobbleFields = 300
)

var (
	videoIDRe      = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	episodeMarkRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bS(?:eason)?\s*(\d{1,3})\s*[:.,\-]?\s*E(?:p(?:isode)?)?\s*(\d{1,4})\b`),
		regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,3})\b`),
		regexp.MustCompile(`(?i)\bseason\s+(\d{1,3})\W+episode\s+(\d{1,4})\b`),
	}
)

type scrobbleIn struct {
	Service    string `json:"service"`
	Title      string `json:"title"`
	Subtitle   string `json:"subtitle"`
	Album      string `json:"album"`
	Artist     string `json:"artist"`
	DurationMS int64  `json:"duration_ms"`
	WatchedAt  string `json:"watched_at"`
}

func parseEpisodeMark(fields ...string) (season, episode int, showCandidates []string, ok bool) {
	pattern := -1
	var loc []int
	var re *regexp.Regexp
	for i, f := range fields {
		for _, r := range episodeMarkRes {
			if l := r.FindStringSubmatchIndex(f); l != nil {
				pattern, loc, re = i, l, r
				break
			}
		}
		if pattern >= 0 {
			break
		}
	}
	if pattern < 0 {
		return 0, 0, nil, false
	}
	m := re.FindStringSubmatch(fields[pattern])
	season, _ = strconv.Atoi(m[1])
	episode, _ = strconv.Atoi(m[2])
	if episode < 1 {
		return 0, 0, nil, false
	}
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.Trim(strings.TrimSpace(s), "-:|–— ")
		if s == "" || seen[strings.ToLower(s)] || len([]rune(s)) > 120 {
			return
		}
		seen[strings.ToLower(s)] = true
		showCandidates = append(showCandidates, s)
	}
	add(fields[pattern][:loc[0]])
	for i, f := range fields {
		if i != pattern {
			add(f)
		}
	}
	add(fields[pattern][loc[1]:])
	return season, episode, showCandidates, true
}

func (a *App) apiIntegrationScrobble(w http.ResponseWriter, r *http.Request) {
	u, err := a.needVerified(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in scrobbleIn
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	for _, f := range []*string{&in.Service, &in.Title, &in.Subtitle, &in.Album, &in.Artist} {
		*f = trunc(strings.TrimSpace(*f), maxScrobbleFields)
	}
	season, episode, candidates, ok := parseEpisodeMark(in.Title, in.Subtitle, in.Album, in.Artist)
	if !ok {
		logger.Info("INTEGRATION scrobble ignored uid=%d service=%q: no season and episode in the media info", u.ID, in.Service)
		a.okJSON(w, map[string]any{"matched": false, "reason": "no_episode_marker"})
		return
	}
	at := ""
	if in.WatchedAt != "" {
		t, perr := time.Parse(time.RFC3339, in.WatchedAt)
		if perr != nil || t.After(time.Now().Add(5*time.Minute)) {
			a.fail(w, r, errBadRequest)
			return
		}
		at = store.Ts(t)
	}
	if err := a.allow(a.writeLimit, "scrobble:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	ctx, cancel := withTimeout(r, 45*time.Second)
	defer cancel()
	if len(candidates) > 3 {
		candidates = candidates[:3]
	}
	for _, c := range candidates {
		ep, sh, status, _ := a.apiResolveEpisode(ctx, u, apiWatchIn{Title: c, Season: season, Episode: episode, Service: in.Service})
		if ep == nil {
			if status >= 500 {
				a.fail(w, r, errTMDBDown)
				return
			}
			continue
		}
		if err := a.Store.MarkWatched(u.ID, ep.ID, at); err != nil {
			logger.Error("INTEGRATION scrobble mark uid=%d ep=%d: %v", u.ID, ep.ID, err)
			a.fail(w, r, err)
			return
		}
		if err := a.ensureFollowing(u.ID, ep.ShowID); err != nil {
			logger.Error("INTEGRATION scrobble follow uid=%d show=%d: %v", u.ID, ep.ShowID, err)
			a.fail(w, r, err)
			return
		}
		a.emitWatch(u, ep, 1)
		logger.Info("INTEGRATION scrobble uid=%d service=%q show=%d s%de%d", u.ID, in.Service, sh.ID, season, episode)
		a.okJSON(w, map[string]any{"matched": true, "show": sh.Name, "season": season, "episode": episode})
		return
	}
	logger.Warn("INTEGRATION scrobble unmatched uid=%d service=%q candidates=%q s%de%d", u.ID, in.Service, candidates, season, episode)
	a.okJSON(w, map[string]any{"matched": false, "reason": "no_exact_title"})
}

func (a *App) apiIntegrationYouTubeSync(w http.ResponseWriter, r *http.Request) {
	u, err := a.needVerified(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		VideoIDs []string `json:"video_ids"`
	}
	if err := decode(r, &in); err != nil || len(in.VideoIDs) > maxSyncVideoIDs {
		a.fail(w, r, errBadRequest)
		return
	}
	ids := make([]string, 0, len(in.VideoIDs))
	seen := map[string]bool{}
	for _, id := range in.VideoIDs {
		if videoIDRe.MatchString(id) && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if err := a.allow(a.writeLimit, "ytsync:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	targets, err := a.Store.YouTubeWatchTargets(u.ID, ids)
	if err != nil {
		logger.Error("INTEGRATION youtube sync targets uid=%d ids=%d: %v", u.ID, len(ids), err)
		a.fail(w, r, err)
		return
	}
	marked, failed := 0, 0
	handled := make([]string, 0, len(ids))
	for _, id := range ids {
		t, found := targets[id]
		if !found || !t.Followed {
			handled = append(handled, id)
			continue
		}
		if err := a.Store.MarkWatched(u.ID, t.EpisodeID, ""); err != nil {
			logger.Error("INTEGRATION youtube sync mark uid=%d ep=%d video=%s: %v", u.ID, t.EpisodeID, id, err)
			failed++
			continue
		}
		if ep, err := a.Store.Episode(t.EpisodeID, 0); err == nil {
			a.emitWatch(u, ep, 1)
		} else {
			logger.Error("INTEGRATION youtube sync episode uid=%d ep=%d: %v", u.ID, t.EpisodeID, err)
		}
		marked++
		handled = append(handled, id)
	}
	logger.Info("INTEGRATION youtube sync uid=%d received=%d marked=%d failed=%d", u.ID, len(ids), marked, failed)
	a.okJSON(w, map[string]any{"marked": marked, "failed": failed, "handled": handled})
}
