package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/CarlSinclair/csuit-libs/apikit"
	"github.com/CarlSinclair/csuit-libs/mcpkit"

	"tvt/auth"
	"tvt/logger"
	"tvt/store"
	"tvt/tmdb"
)

const (
	apiScopeLibraryRead = "library:read"
	apiScopeWatchWrite  = "watch:write"
	apiTokenPrefix      = "tvt_"
	apiMaxKeysPerUser   = 20
)

var apiScopes = []string{apiScopeLibraryRead, apiScopeWatchWrite}

type apiShow struct {
	TMDBID  int64  `json:"tmdb_id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Watched int    `json:"watched"`
	Aired   int    `json:"aired"`
}

type apiUpNext struct {
	TMDBID    int64  `json:"tmdb_id"`
	Show      string `json:"show"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	Title     string `json:"title"`
	AirDate   string `json:"air_date"`
	Remaining int    `json:"remaining"`
}

type apiSearchHit struct {
	TMDBID int64  `json:"tmdb_id"`
	Name   string `json:"name"`
	Year   string `json:"year"`
}

type apiWatchIn struct {
	TMDBID       int64  `json:"tmdb_id"`
	Title        string `json:"title"`
	Season       int    `json:"season"`
	Episode      int    `json:"episode"`
	YouTubeVideo string `json:"youtube_video_id"`
	WatchedAt    string `json:"watched_at"`
	Service      string `json:"service"`
}

type apiWatchOut struct {
	EpisodeID int64  `json:"episode_id"`
	TMDBID    int64  `json:"tmdb_id"`
	Show      string `json:"show"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
}

func isMachineAPI(p string) bool {
	return strings.HasPrefix(p, "/api/v1/") || p == "/mcp"
}

func (a *App) apiKeyStore() *apikit.SQLStore { return &apikit.SQLStore{DB: a.Store.DB} }

func (a *App) registerAPIV1(mux *http.ServeMux) {
	a.apiAuth = apikit.New(apikit.Config{
		Store:    a.apiKeyStore(),
		Enabled:  func() bool { return true },
		ClientIP: auth.ClientIP,
	})
	reg := a.apiAuth.NewRegistry(func(method, path string, h http.Handler) {
		mux.Handle(method+" "+path, h)
	})
	reg.Handle(apikit.Op{Method: http.MethodGet, Path: "/api/v1/shows", Scope: apiScopeLibraryRead, Summary: "List the shows the person follows with their progress", Tag: "Library", Response: apiShow{}, List: true,
		Params: []apikit.Param{{Name: "status", In: "query", Description: "watching, completed, planned, paused or dropped"}}, Tool: "list_shows"}, a.apiV1Shows)
	reg.Handle(apikit.Op{Method: http.MethodGet, Path: "/api/v1/up-next", Scope: apiScopeLibraryRead, Summary: "The next unwatched aired episode of every show being watched", Tag: "Library", Response: apiUpNext{}, List: true, Tool: "up_next"}, a.apiV1UpNext)
	reg.Handle(apikit.Op{Method: http.MethodGet, Path: "/api/v1/search", Scope: apiScopeLibraryRead, Summary: "Search TV shows by title", Tag: "Library", Response: apiSearchHit{}, List: true,
		Params: []apikit.Param{{Name: "q", In: "query", Required: true}}, Tool: "search_shows"}, a.apiV1Search)
	reg.Handle(apikit.Op{Method: http.MethodPost, Path: "/api/v1/watches", Scope: apiScopeWatchWrite, Summary: "Mark an episode watched. Identify it by tmdb_id + season + episode, by title + season + episode (exact title only), or by youtube_video_id. Following the show is implied.", Tag: "Watches",
		Request: apiWatchIn{}, Response: apiWatchOut{}, Status: http.StatusCreated, Tool: "mark_watched"}, a.apiV1Watch)
	reg.CheckScopes(apiScopes)
	mux.Handle("/mcp", &mcpkit.Server{Name: "tvt-tracker", Version: "1.0.0", Tools: reg.MCPTools(), Visible: a.apiAuth.MCPVisible()})
	mux.Handle("GET /api/v1/openapi.json", reg.SpecHandler("TVT API", "1.0.0"))
}

func (a *App) apiOwner(w http.ResponseWriter, k *apikit.Key) (*store.User, bool) {
	u, err := a.Store.UserByID(k.CreatedBy)
	if err != nil {
		logger.Error("API v1 key owner lookup key=%d uid=%d: %v", k.ID, k.CreatedBy, err)
		apikit.WriteError(w, http.StatusForbidden, "forbidden", "the owner of this key no longer exists")
		return nil, false
	}
	if u.IsDeactivated {
		apikit.WriteError(w, http.StatusForbidden, "forbidden", "the owner of this key is deactivated")
		return nil, false
	}
	return u, true
}

func (a *App) apiV1Shows(w http.ResponseWriter, r *http.Request, k *apikit.Key) {
	u, ok := a.apiOwner(w, k)
	if !ok {
		return
	}
	page, ok := apikit.ParsePage(w, r, 50, 200)
	if !ok {
		return
	}
	entries, err := a.Store.UserShows(u.ID, r.URL.Query().Get("status"), page.Limit, int(page.Cursor))
	if err != nil {
		apikit.WriteInternal(w, "list shows", err)
		return
	}
	out := make([]apiShow, 0, len(entries))
	for _, e := range entries {
		out = append(out, apiShow{TMDBID: e.Show.TMDBID, Name: e.Show.Name, Status: e.Follow.Status, Watched: e.Progress.Watched, Aired: e.Progress.Aired})
	}
	var next int64
	if len(entries) == page.Limit {
		next = page.Cursor + int64(len(entries))
	}
	apikit.WriteList(w, out, next)
}

func (a *App) apiV1UpNext(w http.ResponseWriter, r *http.Request, k *apikit.Key) {
	u, ok := a.apiOwner(w, k)
	if !ok {
		return
	}
	items, err := a.Store.UpNext(u.ID, 100)
	if err != nil {
		apikit.WriteInternal(w, "up next", err)
		return
	}
	out := make([]apiUpNext, 0, len(items))
	for _, it := range items {
		out = append(out, apiUpNext{TMDBID: it.Show.TMDBID, Show: it.Show.Name, Season: it.Episode.SeasonNumber, Episode: it.Episode.EpisodeNumber,
			Title: it.Episode.Name, AirDate: it.Episode.AirDate, Remaining: it.Remaining})
	}
	apikit.WriteList(w, out, 0)
}

func (a *App) apiV1Search(w http.ResponseWriter, r *http.Request, k *apikit.Key) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		apikit.WriteError(w, http.StatusBadRequest, "invalid_request", "q must have at least 2 characters")
		return
	}
	ctx, cancel := withTimeout(r, 15*time.Second)
	defer cancel()
	res, err := a.searchTV(ctx, q, "en")
	if err != nil {
		if errors.Is(err, tmdb.ErrNoKey) {
			apikit.WriteError(w, http.StatusServiceUnavailable, "unavailable", "metadata provider is not configured")
			return
		}
		apikit.WriteInternal(w, "search shows", err)
		return
	}
	out := []apiSearchHit{}
	for _, it := range res.Results {
		if len(out) == 10 {
			break
		}
		year := it.FirstAirDate
		if len(year) > 4 {
			year = year[:4]
		}
		out = append(out, apiSearchHit{TMDBID: it.ID, Name: it.Name, Year: year})
	}
	apikit.WriteList(w, out, 0)
}

func normTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (a *App) apiResolveEpisode(ctx context.Context, u *store.User, in apiWatchIn) (*store.Episode, *store.Show, int, string) {
	if in.YouTubeVideo != "" {
		targets, err := a.Store.YouTubeWatchTargets(u.ID, []string{in.YouTubeVideo})
		if err != nil {
			logger.Error("API v1 watch youtube video=%s uid=%d: %v", in.YouTubeVideo, u.ID, err)
			return nil, nil, http.StatusInternalServerError, "could not look the video up"
		}
		t, ok := targets[in.YouTubeVideo]
		if !ok {
			return nil, nil, http.StatusNotFound, "this video is not part of any channel or playlist this server tracks"
		}
		ep, err := a.Store.Episode(t.EpisodeID, 0)
		if err != nil {
			logger.Error("API v1 watch youtube episode=%d uid=%d: %v", t.EpisodeID, u.ID, err)
			return nil, nil, http.StatusInternalServerError, "could not load the episode"
		}
		sh, err := a.Store.ShowByID(ep.ShowID)
		if err != nil {
			logger.Error("API v1 watch youtube show=%d uid=%d: %v", ep.ShowID, u.ID, err)
			return nil, nil, http.StatusInternalServerError, "could not load the show"
		}
		return ep, sh, 0, ""
	}
	if in.Season < 0 || in.Episode < 1 {
		return nil, nil, http.StatusBadRequest, "season and episode are required"
	}
	tmdbID := in.TMDBID
	if tmdbID == 0 {
		title := strings.TrimSpace(in.Title)
		if title == "" {
			return nil, nil, http.StatusBadRequest, "give a tmdb_id, a title or a youtube_video_id"
		}
		res, err := a.searchTV(ctx, title, "en")
		if err != nil {
			logger.Error("API v1 watch search title=%q service=%q uid=%d: %v", title, in.Service, u.ID, err)
			return nil, nil, http.StatusBadGateway, "could not search for the title"
		}
		for _, it := range res.Results {
			if normTitle(it.Name) == normTitle(title) {
				tmdbID = it.ID
				break
			}
		}
		if tmdbID == 0 {
			logger.Warn("API v1 watch no exact title match title=%q service=%q uid=%d", title, in.Service, u.ID)
			return nil, nil, http.StatusNotFound, "no show matches this title exactly"
		}
	}
	sh, err := a.Sync.EnsureShow(ctx, tmdbID)
	if err != nil {
		if errors.Is(err, tmdb.ErrNotFound) {
			return nil, nil, http.StatusNotFound, "show not found"
		}
		logger.Error("API v1 watch ensure show tmdb=%d uid=%d: %v", tmdbID, u.ID, err)
		return nil, nil, http.StatusBadGateway, "could not load the show"
	}
	ep, err := a.Store.EpisodeByNumber(sh.ID, in.Season, in.Episode)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, http.StatusNotFound, "episode not found"
		}
		logger.Error("API v1 watch episode show=%d s%de%d uid=%d: %v", sh.ID, in.Season, in.Episode, u.ID, err)
		return nil, nil, http.StatusInternalServerError, "could not load the episode"
	}
	return ep, sh, 0, ""
}

func (a *App) apiV1Watch(w http.ResponseWriter, r *http.Request, k *apikit.Key) {
	u, ok := a.apiOwner(w, k)
	if !ok {
		return
	}
	var in apiWatchIn
	if !apikit.DecodeJSON(w, r, &in) {
		return
	}
	at := ""
	if in.WatchedAt != "" {
		t, err := time.Parse(time.RFC3339, in.WatchedAt)
		if err != nil || t.After(time.Now().Add(5*time.Minute)) {
			apikit.WriteError(w, http.StatusBadRequest, "invalid_request", "watched_at must be an RFC 3339 time that is not in the future")
			return
		}
		at = store.Ts(t)
	}
	ctx, cancel := withTimeout(r, 45*time.Second)
	defer cancel()
	ep, sh, status, msg := a.apiResolveEpisode(ctx, u, in)
	if ep == nil {
		code := "not_found"
		if status == http.StatusBadRequest {
			code = "invalid_request"
		} else if status >= 500 {
			code = "unavailable"
		}
		apikit.WriteError(w, status, code, msg)
		return
	}
	if err := a.Store.MarkWatched(u.ID, ep.ID, at); err != nil {
		apikit.WriteInternal(w, "mark watched", err)
		return
	}
	if err := a.ensureFollowing(u.ID, ep.ShowID); err != nil {
		apikit.WriteInternal(w, "follow after watch", err)
		return
	}
	a.emitWatch(u, ep, 1)
	logger.Info("API v1 watch uid=%d key=%d show=%d s%de%d service=%q", u.ID, k.ID, sh.ID, ep.SeasonNumber, ep.EpisodeNumber, in.Service)
	apikit.WriteJSON(w, http.StatusCreated, apiWatchOut{EpisodeID: ep.ID, TMDBID: sh.TMDBID, Show: sh.Name, Season: ep.SeasonNumber, Episode: ep.EpisodeNumber})
}

type apiKeyView struct {
	ID         int64    `json:"id"`
	Label      string   `json:"label"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes"`
	CreatedAt  string   `json:"created_at"`
	LastUsedAt string   `json:"last_used_at"`
}

func (a *App) apiAccountKeys(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	keys, err := a.apiKeyStore().List(r.Context(), u.ID)
	if err != nil {
		logger.Error("API keys list uid=%d: %v", u.ID, err)
		a.fail(w, r, err)
		return
	}
	out := make([]apiKeyView, 0, len(keys))
	for _, k := range keys {
		v := apiKeyView{ID: k.ID, Label: k.Label, Prefix: k.Prefix, Scopes: k.Scopes, CreatedAt: store.Ts(k.CreatedAt)}
		if k.LastUsedAt != nil {
			v.LastUsedAt = store.Ts(*k.LastUsedAt)
		}
		if k.Revoked() || k.Expired(time.Now()) {
			continue
		}
		out = append(out, v)
	}
	a.okJSON(w, map[string]any{"keys": out, "scopes": apiScopes})
}

func (a *App) apiAccountKeyCreate(w http.ResponseWriter, r *http.Request) {
	u, err := a.needVerified(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Label  string   `json:"label"`
		Scopes []string `json:"scopes"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	label := strings.TrimSpace(in.Label)
	if label == "" || len([]rune(label)) > 100 {
		a.fail(w, r, errBadRequest)
		return
	}
	scopes, err := apikit.NormalizeScopes(in.Scopes, apiScopes)
	if err != nil {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.allow(a.mailLimit, "apikey:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	existing, err := a.apiKeyStore().List(r.Context(), u.ID)
	if err != nil {
		logger.Error("API key create list uid=%d: %v", u.ID, err)
		a.fail(w, r, err)
		return
	}
	active := 0
	for _, k := range existing {
		if !k.Revoked() && !k.Expired(time.Now()) {
			active++
		}
	}
	if active >= apiMaxKeysPerUser {
		a.fail(w, r, errBadRequest)
		return
	}
	raw, hash, display, err := apikit.NewToken(apiTokenPrefix)
	if err != nil {
		logger.Error("API key create token uid=%d: %v", u.ID, err)
		a.fail(w, r, err)
		return
	}
	id, err := a.apiKeyStore().Insert(r.Context(), apikit.Key{Label: label, Prefix: display, Scopes: scopes, CreatedBy: u.ID, CreatedAt: time.Now().UTC()}, hash)
	if err != nil {
		logger.Error("API key create insert uid=%d: %v", u.ID, err)
		a.fail(w, r, err)
		return
	}
	logger.Info("API key created uid=%d key=%d scopes=%v", u.ID, id, scopes)
	a.okJSON(w, map[string]any{"id": id, "key": raw})
}

func (a *App) apiAccountKeyRevoke(w http.ResponseWriter, r *http.Request) {
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
	done, err := a.apiKeyStore().Revoke(r.Context(), id, u.ID, time.Now().UTC())
	if err != nil {
		logger.Error("API key revoke uid=%d key=%d: %v", u.ID, id, err)
		a.fail(w, r, err)
		return
	}
	if !done {
		a.fail(w, r, errNotFound)
		return
	}
	logger.Info("API key revoked uid=%d key=%d", u.ID, id)
	a.okJSON(w, nil)
}
