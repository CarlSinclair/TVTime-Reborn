package handlers

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"tvt/assets"
	"tvt/auth"
	"tvt/logger"
	"tvt/store"
	"tvt/tmdb"
	"tvt/tvdb"
)

var assetVersion = strconv.FormatInt(time.Now().Unix(), 10)

func (a *App) routes(mux *http.ServeMux) {
	sub, err := fs.Sub(assets.FS, "static")
	if err != nil {
		logger.Fatal("static fs: %v", err)
	}
	files := http.FileServerFS(sub)
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheControl(files)))
	mux.HandleFunc("GET /sw.js", a.serviceWorker)
	mux.HandleFunc("GET /manifest.webmanifest", a.manifest)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Store.DB.PingContext(r.Context()); err != nil {
			logger.Error("HEALTH db ping: %v", err)
			http.Error(w, "db", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /{$}", a.pageHome)
	mux.HandleFunc("GET /welcome", a.pageLanding)
	mux.HandleFunc("GET /{slug}", a.pagePublic)
	mux.HandleFunc("POST /api/contact", a.apiContact)
	mux.HandleFunc("POST /api/admin/pages", a.apiAdminPages)
	mux.HandleFunc("GET /search", a.pageSearch)
	mux.HandleFunc("GET /show/{tmdb}", a.pageShow)
	mux.HandleFunc("GET /show/{tmdb}/season/{n}", a.pageSeason)
	mux.HandleFunc("GET /episode/{id}", a.pageEpisode)
	mux.HandleFunc("GET /shows", a.pageMyShows)
	mux.HandleFunc("GET /calendar", a.pageCalendar)
	mux.HandleFunc("GET /settings", a.pageSettings)
	mux.HandleFunc("GET /notifications", a.pageNotifications)
	mux.HandleFunc("GET /notifications/{id}/open", a.pageNotificationOpen)
	mux.HandleFunc("GET /u/{username}", a.pageProfile)
	mux.HandleFunc("GET /profile", a.pageOwnProfile)
	mux.HandleFunc("GET /media/{kind}/{file}", a.serveMedia)
	mux.HandleFunc("GET /img/{size}/{file}", a.serveImage)
	mux.HandleFunc("GET /tvdbimg/{file}", a.serveTVDBImage)
	mux.HandleFunc("GET /auth/telegram", a.pageTelegramLogin)
	mux.HandleFunc("GET /login/2fa", a.pageMFA)
	mux.HandleFunc("GET /login", a.pageSimple("login", "auth.login"))
	mux.HandleFunc("GET /register", a.pageSimple("register", "auth.register"))
	mux.HandleFunc("GET /forgot", a.pageSimple("forgot", "auth.forgot"))
	mux.HandleFunc("GET /privacy", a.pagePrivacy)
	mux.HandleFunc("GET /about", a.pageSimple("about", "about.title"))
	mux.HandleFunc("GET /offline", a.pageSimple("offline", "offline.title"))
	mux.HandleFunc("GET /t/{purpose}", a.pageToken)
	mux.HandleFunc("GET /admin", a.pageAdmin)
	mux.HandleFunc("GET /admin/reports", a.adminRedirect("reports"))
	mux.HandleFunc("GET /admin/servers", a.adminRedirect("servers"))
	mux.HandleFunc("POST /api/admin/servers/add", a.apiAdminServerAdd)
	mux.HandleFunc("POST /api/admin/users", a.apiAdminUserCreate)
	mux.HandleFunc("POST /api/admin/users/{id}/update", a.apiAdminUserUpdate)
	mux.HandleFunc("POST /api/admin/users/{id}/delete", a.apiAdminUserDelete)
	mux.HandleFunc("POST /api/admin/users/{id}/deactivate", a.apiAdminDeactivate)
	mux.HandleFunc("POST /api/admin/users/{id}/reset-password", a.apiAdminResetLink)
	mux.HandleFunc("POST /api/admin/signups", a.apiAdminSignups)
	mux.HandleFunc("POST /api/admin/logo", a.apiAdminLogoUpload)
	mux.HandleFunc("POST /api/admin/logo/remove", a.apiAdminLogoRemove)
	mux.HandleFunc("POST /api/admin/config", a.apiAdminConfigSave)
	mux.HandleFunc("POST /api/admin/config/test", a.apiAdminConfigTest)
	mux.HandleFunc("POST /api/admin/restart", a.apiAdminRestart)
	mux.HandleFunc("POST /api/admin/servers/share", a.apiAdminServerShare)
	mux.HandleFunc("POST /api/remote-comments/{id}/report", a.apiReportRemoteComment)
	mux.HandleFunc("POST /api/admin/servers/block", a.apiAdminServer(false))
	mux.HandleFunc("POST /api/admin/servers/forget", a.apiAdminServer(true))

	mux.HandleFunc("POST /api/auth/register", a.apiRegister)
	mux.HandleFunc("POST /api/auth/login", a.apiLogin)
	mux.HandleFunc("POST /api/auth/logout", a.apiLogout)
	mux.HandleFunc("POST /api/auth/magic", a.apiMagicRequest)
	mux.HandleFunc("POST /api/auth/forgot", a.apiForgot)
	mux.HandleFunc("POST /api/auth/resend-verify", a.apiResendVerify)
	mux.HandleFunc("POST /api/auth/verify/consume", a.apiVerifyConsume)
	mux.HandleFunc("POST /api/auth/magic/consume", a.apiMagicConsume)
	mux.HandleFunc("POST /api/auth/reset/consume", a.apiResetConsume)
	mux.HandleFunc("POST /api/auth/2fa", a.apiMFAVerify)
	mux.HandleFunc("POST /api/auth/passkey/begin", a.apiPasskeyLoginBegin)
	mux.HandleFunc("POST /api/auth/passkey/finish", a.apiPasskeyLoginFinish)
	mux.HandleFunc("POST /api/account/passkey/begin", a.apiPasskeyAddBegin)
	mux.HandleFunc("POST /api/account/passkey/finish", a.apiPasskeyAddFinish)
	mux.HandleFunc("POST /api/account/passkey/{id}/delete", a.apiPasskeyDelete)

	mux.HandleFunc("POST /api/account/locale", a.apiSetLocale)
	mux.HandleFunc("POST /api/account/password", a.apiChangePassword)
	mux.HandleFunc("POST /api/account/email", a.apiAddEmail)
	mux.HandleFunc("POST /api/account/email/change", a.apiChangeEmail)
	mux.HandleFunc("POST /api/auth/emailchange/consume", a.apiEmailChangeConsume)
	mux.HandleFunc("POST /api/account/timezone", a.apiSetTimezone)
	mux.HandleFunc("POST /api/account/logout-everywhere", a.apiLogoutEverywhere)
	mux.HandleFunc("POST /api/account/2fa/setup", a.apiTOTPSetup)
	mux.HandleFunc("POST /api/account/2fa/enable", a.apiTOTPEnable)
	mux.HandleFunc("POST /api/account/2fa/disable", a.apiTOTPDisable)
	mux.HandleFunc("POST /api/account/2fa/recovery", a.apiRecoveryRegenerate)
	mux.HandleFunc("POST /api/account/telegram/link-token", a.apiTelegramLinkToken)
	mux.HandleFunc("GET /api/account/telegram/status", a.apiTelegramStatus)
	mux.HandleFunc("POST /api/account/telegram/unlink-chat", a.apiTelegramUnlinkChat)
	mux.HandleFunc("POST /api/account/telegram/unlink", a.apiTelegramUnlinkIdentity)
	mux.HandleFunc("POST /api/account/telegram/enable-dm", a.apiTelegramEnableDM)
	mux.HandleFunc("POST /api/telegram/webhook", a.handleTelegramWebhook)
	mux.HandleFunc("POST /api/profile", a.apiUpdateProfile)
	mux.HandleFunc("POST /api/profile/avatar", a.apiUploadMedia("avatars"))
	mux.HandleFunc("POST /api/profile/banner", a.apiUploadMedia("banners"))
	mux.HandleFunc("POST /api/profile/avatar/remove", a.apiRemoveMedia("avatars"))
	mux.HandleFunc("POST /api/profile/banner/remove", a.apiRemoveMedia("banners"))
	mux.HandleFunc("GET /api/notifications/recent", a.apiNotificationsRecent)
	mux.HandleFunc("GET /api/notifications/stream", a.apiNotificationsStream)
	mux.HandleFunc("POST /api/notifications/read-all", a.apiNotificationsReadAll)
	mux.HandleFunc("POST /api/notifications/settings", a.apiNotificationSettings)
	mux.HandleFunc("POST /api/import/tvtime", a.apiImportTVTime)
	mux.HandleFunc("GET /api/import/status", a.apiImportStatus)
	mux.HandleFunc("POST /api/notifications/test", a.apiNotificationTest)
	mux.HandleFunc("POST /api/notifications/timing", a.apiNotificationTiming)
	mux.HandleFunc("POST /api/notifications/{id}/read", a.apiNotificationRead)
	mux.HandleFunc("GET /api/push/key", a.apiPushKey)
	mux.HandleFunc("POST /api/push/subscribe", a.apiPushSubscribe)
	mux.HandleFunc("POST /api/push/unsubscribe", a.apiPushUnsubscribe)
	mux.HandleFunc("POST /api/account/delete", a.apiDeleteAccount)
	mux.HandleFunc("POST /api/account/deactivate", a.apiDeactivateAccount)
	mux.HandleFunc("POST /api/account/username", a.apiChangeUsername)
	mux.HandleFunc("GET /api/account/export", a.apiExport)

	mux.HandleFunc("GET /api/search", a.apiSearch)
	if a.Fed != nil {
		mux.HandleFunc("GET /.well-known/tvt", a.Fed.WellKnown)
		mux.HandleFunc("GET /federation/user/{name}", a.Fed.UserInfo)
		mux.HandleFunc("POST /federation/inbox", a.Fed.Inbox)
		mux.HandleFunc("POST /federation/renew", a.Fed.Renew)
	}
	mux.HandleFunc("GET /api/people", a.apiPeople)
	mux.HandleFunc("GET /api/friends", a.apiFriendsList)
	mux.HandleFunc("POST /api/friends/request", a.apiFriendRequest)
	mux.HandleFunc("POST /api/friends/respond", a.apiFriendRespond)
	mux.HandleFunc("POST /api/friends/remove", a.apiFriendRemove)
	mux.HandleFunc("GET /api/feed", a.apiFeed)
	mux.HandleFunc("POST /api/users/block", a.apiBlockUser)
	mux.HandleFunc("POST /api/users/report", a.apiReportUser)
	mux.HandleFunc("POST /api/admin/users/{id}/dismiss-reports", a.apiAdminDismissUser)
	mux.HandleFunc("POST /api/show/{tmdb}/follow", a.apiFollow)
	mux.HandleFunc("POST /api/show/{tmdb}/unfollow", a.apiUnfollow)
	mux.HandleFunc("POST /api/show/{tmdb}/favorite", a.apiFavorite)
	mux.HandleFunc("POST /api/show/{tmdb}/season/{n}/watch", a.apiWatchSeason)
	mux.HandleFunc("POST /api/episode/{id}/watch", a.apiWatch)
	mux.HandleFunc("POST /api/episode/{id}/rate", a.apiRate)

	mux.HandleFunc("GET /api/episode/{id}/comments", a.apiListComments())
	mux.HandleFunc("POST /api/episode/{id}/comments", a.apiAddComment())
	mux.HandleFunc("GET /api/profile/{id}/comments", a.apiListComments())
	mux.HandleFunc("POST /api/profile/{id}/comments", a.apiAddComment())
	mux.HandleFunc("POST /api/comments/{id}/edit", a.apiEditComment)
	mux.HandleFunc("POST /api/comments/{id}/delete", a.apiDeleteComment)
	mux.HandleFunc("POST /api/comments/{id}/report", a.apiReportComment)
	mux.HandleFunc("POST /api/comments/{id}/block", a.apiBlockAuthor)
	mux.HandleFunc("POST /api/blocks/unblock", a.apiUnblock)

	mux.HandleFunc("POST /api/admin/comments/{id}/delete", a.apiAdminDelete)
	mux.HandleFunc("POST /api/admin/comments/{id}/resolve", a.apiAdminResolve)
	mux.HandleFunc("POST /api/admin/users/{id}/ban", a.apiAdminBan)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		a.errorPage(w, r, http.StatusNotFound, "err.not_found")
	})
}

func cacheControl(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		h.ServeHTTP(w, r)
	})
}

func (a *App) serviceWorker(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(assets.FS, "static/sw.js")
	if err != nil {
		logger.Error("SW read: %v", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	body := strings.ReplaceAll(string(data), "__VERSION__", assetVersion)
	w.Write([]byte(body))
}

func (a *App) manifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	m := map[string]any{
		"id": "/", "name": a.Cfg.AppName, "short_name": a.Cfg.AppName, "start_url": "/", "scope": "/", "display": "standalone",
		"description": "Track the shows you watch, see what is next and talk about each episode.", "categories": []string{"entertainment"},
		"background_color": "#0f1117", "theme_color": "#7c5cff",
		"icons": []map[string]string{
			{"src": "/static/img/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "/static/img/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			{"src": "/static/img/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
			{"src": "/static/img/icon.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any"},
		},
	}
	if err := json.NewEncoder(w).Encode(m); err != nil {
		logger.Warn("manifest write: %v", err)
	}
}

func (a *App) pageSimple(name, titleKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if (name == "login" || name == "register" || name == "forgot") && auth.UserFrom(r.Context()) != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		data := map[string]any{}
		if code := r.URL.Query().Get("error"); code != "" {
			switch code {
			case "telegram_failed", "telegram_unavailable", "deactivated", "rate_limited", "internal", "signups_closed":
				data["ErrorKey"] = "err." + code
			}
		}
		a.page(w, r, name, titleKey, data)
	}
}

func (a *App) pageToken(w http.ResponseWriter, r *http.Request) {
	purpose := r.PathValue("purpose")
	if purpose != auth.PurposeLogin && purpose != auth.PurposeReset && purpose != auth.PurposeVerify && purpose != auth.PurposeEmail {
		a.errorPage(w, r, http.StatusNotFound, "err.not_found")
		return
	}
	a.page(w, r, "token", "token."+purpose+".title", map[string]any{"Purpose": purpose, "Token": r.URL.Query().Get("token")})
}

func (a *App) langOf(w http.ResponseWriter, r *http.Request) string {
	return a.lang(w, r, auth.UserFrom(r.Context()))
}

// pageLanding is the public front page: what TVT is and how to join. Visitors
// see it at the root; everyone can open it at /welcome.
func (a *App) pageLanding(w http.ResponseWriter, r *http.Request) {
	a.page(w, r, "landing", "home.hero_title", map[string]any{})
}

func (a *App) pageHome(w http.ResponseWriter, r *http.Request) {
	if auth.UserFrom(r.Context()) == nil {
		a.pageLanding(w, r)
		return
	}
	lang := a.langOf(w, r)
	data := map[string]any{"NoMetadata": !a.TMDB.Configured()}
	if u := auth.UserFrom(r.Context()); u != nil {
		next, err := a.Store.UpNext(u.ID, 60)
		if err != nil {
			logger.Error("PAGE home up next uid=%d: %v", u.ID, err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
			return
		}
		a.localizeUpNext(lang, next)
		data["UpNext"] = next
	}
	a.page(w, r, "home", "nav.home", data)
}

func (a *App) pageSearch(w http.ResponseWriter, r *http.Request) {
	lang := a.langOf(w, r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	data := map[string]any{"Query": q}
	kind := r.URL.Query().Get("kind")
	if strings.HasPrefix(q, "@") {
		kind = "people"
	}
	data["Kind"] = kind
	if kind == "people" {
		if viewer := auth.UserFrom(r.Context()); viewer != nil && q != "" {
			if err := a.allow(a.searchLimit, "p:"+itoa(viewer.ID)); err != nil {
				a.errorPage(w, r, http.StatusTooManyRequests, "err.rate_limited")
				return
			}
			u, remote, err := a.findPerson(viewer, q)
			if err != nil {
				logger.Error("PAGE search people q=%q: %v", q, err)
				a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
				return
			}
			data["Remote"] = remote
			if u != nil {
				pv := a.personJSON(store.Person{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, AvatarPath: u.AvatarPath})
				pv.Self = u.ID == viewer.ID
				if !pv.Self {
					if pv.State, err = a.Store.FriendState(viewer.ID, u.ID); err != nil {
						logger.Error("PAGE search people state: %v", err)
						a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
						return
					}
				}
				data["Person"] = pv
			}
		}
		a.page(w, r, "search", "nav.search", data)
		return
	}
	if q != "" {
		if err := a.allow(a.searchLimit, "s:"+auth.ClientIP(r)); err != nil {
			a.errorPage(w, r, http.StatusTooManyRequests, "err.rate_limited")
			return
		}
		ctx, cancel := withTimeout(r, 15*time.Second)
		defer cancel()
		shows, err := a.searchTV(ctx, q, lang)
		switch {
		case errors.Is(err, tmdb.ErrNoKey):
			local, lerr := a.Store.SearchLocal(q, 30)
			if lerr != nil {
				logger.Error("PAGE search local q=%q: %v", q, lerr)
			}
			data["Local"] = local
			data["NoMetadata"] = true
		case err != nil:
			logger.Error("PAGE search q=%q: %v", q, err)
			data["SearchFailed"] = true
		default:
			data["Results"] = shows.Results
		}
	}
	a.page(w, r, "search", "nav.search", data)
}

func (a *App) apiSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		a.okJSON(w, map[string]any{"results": []any{}})
		return
	}
	if err := a.allow(a.searchLimit, "s:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	lang := a.langOf(w, r)
	ctx, cancel := withTimeout(r, 15*time.Second)
	defer cancel()
	res, err := a.searchTV(ctx, q, lang)
	if err != nil {
		if errors.Is(err, tmdb.ErrNoKey) {
			a.fail(w, r, errTMDBMissing)
		} else {
			logger.Error("API search q=%q: %v", q, err)
			a.fail(w, r, errTMDBDown)
		}
		return
	}
	items := res.Results
	if len(items) > 8 {
		items = items[:8]
	}
	a.okJSON(w, map[string]any{"results": items})
}

func (a *App) loadShow(w http.ResponseWriter, r *http.Request) (*store.Show, string, bool) {
	lang := a.langOf(w, r)
	tid, err := strconv.ParseInt(r.PathValue("tmdb"), 10, 64)
	if err != nil || tid == 0 {
		a.errorPage(w, r, http.StatusNotFound, "err.not_found")
		return nil, lang, false
	}
	ctx, cancel := withTimeout(r, 45*time.Second)
	defer cancel()
	sh, err := a.Sync.EnsureShow(ctx, tid)
	switch {
	case err == nil:
		a.Sync.EnsureShowLang(ctx, sh, lang)
		if lerr := a.Store.LocalizeShows(lang, sh); lerr != nil {
			logger.Error("PAGE localize show=%d lang=%s: %v", sh.ID, lang, lerr)
		}
		return sh, lang, true
	case errors.Is(err, tmdb.ErrNotFound), errors.Is(err, tvdb.ErrNotFound):
		a.errorPage(w, r, http.StatusNotFound, "err.not_found")
	case errors.Is(err, tmdb.ErrNoKey), errors.Is(err, tvdb.ErrNoKey):
		a.errorPage(w, r, http.StatusServiceUnavailable, "err.metadata_not_configured")
	default:
		logger.Error("PAGE load show tmdb=%d: %v", tid, err)
		a.errorPage(w, r, http.StatusBadGateway, "err.metadata_unavailable")
	}
	return nil, lang, false
}

func (a *App) pageShow(w http.ResponseWriter, r *http.Request) {
	sh, _, ok := a.loadShow(w, r)
	if !ok {
		return
	}
	seasons, err := a.Store.Seasons(sh.ID)
	if err != nil {
		logger.Error("PAGE show seasons show=%d: %v", sh.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	data := map[string]any{"Show": sh, "Seasons": seasons, "SeasonProgress": map[int]store.Progress{}}
	if list, err := a.Store.ShowRatings(sh.ID); err != nil {
		logger.Error("PAGE show ratings show=%d: %v", sh.ID, err)
	} else {
		data["Chart"] = buildRatingChart(list)
	}
	if u := auth.UserFrom(r.Context()); u != nil {
		f, err := a.Store.FollowOf(u.ID, sh.ID)
		if err != nil {
			logger.Error("PAGE show follow: %v", err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
			return
		}
		p, err := a.Store.Progress(u.ID, sh.ID)
		if err != nil {
			logger.Error("PAGE show progress: %v", err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
			return
		}
		sp, err := a.Store.SeasonProgress(u.ID, sh.ID)
		if err != nil {
			logger.Error("PAGE show season progress: %v", err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
			return
		}
		data["Follow"] = f
		data["Progress"] = p
		data["SeasonProgress"] = sp
	}
	a.render(w, r, "show", http.StatusOK, sh.Name, data)
}

func (a *App) pageSeason(w http.ResponseWriter, r *http.Request) {
	sh, lang, ok := a.loadShow(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 {
		a.errorPage(w, r, http.StatusNotFound, "err.not_found")
		return
	}
	var uid int64
	if u := auth.UserFrom(r.Context()); u != nil {
		uid = u.ID
	}
	eps, err := a.Store.Episodes(sh.ID, n, uid)
	if err != nil {
		logger.Error("PAGE season show=%d season=%d: %v", sh.ID, n, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	if len(eps) == 0 {
		a.errorPage(w, r, http.StatusNotFound, "err.not_found")
		return
	}
	ptrs := make([]*store.Episode, len(eps))
	for i := range eps {
		ptrs[i] = &eps[i]
	}
	if err := a.Store.LocalizeEpisodes(lang, ptrs...); err != nil {
		logger.Error("PAGE localize episodes show=%d lang=%s: %v", sh.ID, lang, err)
	}
	seasons, err := a.Store.Seasons(sh.ID)
	if err != nil {
		logger.Error("PAGE season list show=%d: %v", sh.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	watched := 0
	for _, e := range eps {
		if e.Watched {
			watched++
		}
	}
	a.render(w, r, "season", http.StatusOK, sh.Name, map[string]any{"Show": sh, "Season": n, "Episodes": eps, "Seasons": seasons, "WatchedCount": watched})
}

func (a *App) pageEpisode(w http.ResponseWriter, r *http.Request) {
	lang := a.langOf(w, r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.errorPage(w, r, http.StatusNotFound, "err.not_found")
		return
	}
	var uid int64
	if u := auth.UserFrom(r.Context()); u != nil {
		uid = u.ID
	}
	ep, err := a.Store.Episode(id, uid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.errorPage(w, r, http.StatusNotFound, "err.not_found")
		} else {
			logger.Error("PAGE episode id=%d: %v", id, err)
			a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		}
		return
	}
	sh, err := a.Store.ShowByID(ep.ShowID)
	if err != nil {
		logger.Error("PAGE episode show=%d: %v", ep.ShowID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	ctx, cancel := withTimeout(r, 30*time.Second)
	defer cancel()
	a.Sync.EnsureShowLang(ctx, sh, lang)
	if err := a.Store.LocalizeShows(lang, sh); err != nil {
		logger.Error("PAGE localize episode show=%d lang=%s: %v", sh.ID, lang, err)
	}
	prev, next, err := a.Store.Adjacent(sh.ID, ep.SeasonNumber, ep.EpisodeNumber)
	if err != nil {
		logger.Error("PAGE episode adjacent: %v", err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	group := []*store.Episode{ep}
	if prev != nil {
		group = append(group, prev)
	}
	if next != nil {
		group = append(group, next)
	}
	if err := a.Store.LocalizeEpisodes(lang, group...); err != nil {
		logger.Error("PAGE localize episode id=%d lang=%s: %v", ep.ID, lang, err)
	}
	a.render(w, r, "episode", http.StatusOK, sh.Name+" "+episodeCode(ep.SeasonNumber, ep.EpisodeNumber),
		map[string]any{"Show": sh, "Episode": ep, "Prev": prev, "Next": next})
}

func (a *App) pageMyShows(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "all" && !store.ValidStatus(status) {
		status = ""
	}
	list, err := a.Store.UserShows(u.ID, status)
	if err != nil {
		logger.Error("PAGE my shows uid=%d: %v", u.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	a.localizeEntries(a.langOf(w, r), list)
	counts, err := a.Store.StatusCounts(u.ID)
	if err != nil {
		logger.Error("PAGE my shows counts uid=%d: %v", u.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	a.page(w, r, "myshows", "nav.shows", map[string]any{"Counts": counts, "Entries": list, "Status": status, "Statuses": []string{"watching", "plan", "completed", "paused", "dropped"}})
}

func (a *App) pageCalendar(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	now := time.Now().UTC()
	past := r.URL.Query().Get("past") == "1"
	from := now.Format("2006-01-02")
	to := now.AddDate(0, 0, 60).Format("2006-01-02")
	if past {
		from = now.AddDate(0, 0, -45).Format("2006-01-02")
		to = now.AddDate(0, 0, -1).Format("2006-01-02")
	}
	items, err := a.Store.Calendar(u.ID, from, to)
	if err != nil {
		logger.Error("PAGE calendar uid=%d: %v", u.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	lang := a.langOf(w, r)
	a.localizeCalendar(lang, items)
	type day struct {
		Date  string
		Items []store.CalendarItem
	}
	byDate := map[string]*day{}
	var dates []string
	get := func(d string) *day {
		if x, ok := byDate[d]; ok {
			return x
		}
		x := &day{Date: d}
		byDate[d] = x
		dates = append(dates, d)
		return x
	}
	for _, it := range items {
		d := get(it.Episode.AirDate)
		d.Items = append(d.Items, it)
	}
	sort.Strings(dates)
	if past {
		sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	}
	days := make([]*day, 0, len(dates))
	for _, d := range dates {
		days = append(days, byDate[d])
	}
	a.page(w, r, "calendar", "nav.calendar", map[string]any{"Days": days, "Today": now.Format("2006-01-02"), "Past": past})
}

const privacyUpdated = "2026-10-13"

var privacySections = []string{"data", "signin", "cookies", "third", "public", "admintools", "use", "retention", "rights", "children", "changes", "contact"}

func (a *App) pagePrivacy(w http.ResponseWriter, r *http.Request) {
	lang := a.langOf(w, r)
	if p, ok := a.resolvePage("privacy", lang); ok {
		a.renderPage(w, r, p)
		return
	}
	operator := a.Cfg.OperatorName
	if operator == "" {
		operator = a.I18n.T(lang, "privacy.operator_default")
	}
	contact := a.Cfg.PrivacyContact
	if contact == "" {
		contact = a.I18n.T(lang, "privacy.contact_none")
	}
	a.page(w, r, "privacy", "privacy.title", map[string]any{"Sections": privacySections, "Operator": operator, "Contact": contact, "Updated": privacyUpdated})
}
