package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"tvt/appconfig"
	"tvt/assets"
	"tvt/auth"
	"tvt/config"
	"tvt/federation"
	"tvt/i18n"
	"tvt/importer"
	"tvt/logger"
	"tvt/mailer"
	"tvt/media"
	"tvt/notify"
	"tvt/store"
	"tvt/syncer"
	"tvt/telegram"
	"tvt/tmdb"
)

type App struct {
	Cfg   *config.Config
	Store *store.Store
	Auth  *auth.Service
	Sync  *syncer.Syncer
	TMDB  *tmdb.Client
	Mail  *mailer.Mailer
	I18n  *i18n.Bundle
	Notif *notify.Service
	Bot   *telegram.Bot
	Media media.Store

	Importer *importer.Runner
	Fed      *federation.Service
	// Settings holds the options an admin may change from the web UI; Restart,
	// when set, stops the process cleanly with a non-zero status.
	Settings     *appconfig.Manager
	logo         logoState
	pageCache    pageCache
	contactLimit *auth.Limiter
	supportLimit *auth.Limiter
	Restart      func()

	tmpl     map[string]*template.Template
	mfaLimit *auth.Limiter

	authLimit    *auth.Limiter
	mailLimit    *auth.Limiter
	commentLimit *auth.Limiter
	reportLimit  *auth.Limiter
	searchLimit  *auth.Limiter
	imgLimit     *auth.Limiter
	pageLimit    *auth.Limiter
	writeLimit   *auth.Limiter
	trending     trendingCache
}

func New(cfg *config.Config, st *store.Store, a *auth.Service, sy *syncer.Syncer, tm *tmdb.Client, ml *mailer.Mailer, nt *notify.Service, bot *telegram.Bot, md media.Store) (*App, error) {
	bundle, err := i18n.Load(assets.FS, "locales", cfg.DefaultLang)
	if err != nil {
		return nil, err
	}
	ml.Record = func(to, subject string, err error) {
		if lerr := st.LogMail(to, subject, err); lerr != nil {
			logger.Error("MAIL log subject=%q: %v", subject, lerr)
		}
	}
	app := &App{
		Cfg: cfg, Store: st, Auth: a, Sync: sy, TMDB: tm, Mail: ml, I18n: bundle, Notif: nt, Bot: bot, Media: md,
		mfaLimit:     auth.NewLimiter(8, 10*time.Minute),
		authLimit:    auth.NewLimiter(15, time.Minute),
		mailLimit:    auth.NewLimiter(5, time.Hour),
		commentLimit: auth.NewLimiter(8, time.Minute),
		reportLimit:  auth.NewLimiter(40, time.Hour),
		searchLimit:  auth.NewLimiter(40, time.Minute),
		imgLimit:     auth.NewLimiter(600, time.Minute),
		pageLimit:    auth.NewLimiter(600, time.Minute),
		writeLimit:   auth.NewLimiter(120, time.Minute),
		contactLimit: auth.NewLimiter(3, time.Hour),
		supportLimit: auth.NewLimiter(60, time.Hour),
	}
	app.logo.set(st.KVGet(logoKV))
	app.reloadPages()
	app.Importer = &importer.Runner{Store: st, Sync: sy, TMDB: tm, Notify: nt}
	if n, err := st.FailInterruptedImports(); err != nil {
		logger.Error("IMPORT could not clear interrupted jobs: %v", err)
	} else if n > 0 {
		logger.Warn("IMPORT marked %d interrupted job(s) as failed", n)
	}
	if err := st.PruneImports(180 * 24 * time.Hour); err != nil {
		logger.Error("IMPORT prune: %v", err)
	}
	if err := app.loadTemplates(); err != nil {
		return nil, err
	}
	return app, nil
}

type View struct {
	App     *App
	Lang    string
	User    *store.User
	CSRF    string
	Title   string
	Path    string
	Data    map[string]any
	Unread  int
	Friends int
	// AdminOpen is the number of admin items waiting, shown beside the Admin link.
	AdminOpen int
}

var LangNames = map[string]string{
	"en": "English", "pt": "Português", "es": "Español", "it": "Italiano", "fr": "Français",
	"zh": "中文", "ru": "Русский", "ja": "日本語", "ko": "한국어", "ar": "العربية",
}

func (v *View) LangName(code string) string {
	if n, ok := LangNames[code]; ok {
		return n
	}
	return code
}

func (v *View) Dir() string {
	if v.Lang == "ar" {
		return "rtl"
	}
	return "ltr"
}

func (v *View) PasskeysEnabled() bool { return v.App.Auth.PasskeysEnabled() }

func (v *View) SourceURL() string { return v.App.Cfg.SourceURL }

func (v *View) TelegramBotID() string { return v.App.Bot.BotID() }

// TN renders a named-placeholder string: TN("key", "name", "value", ...).
func (v *View) TN(key string, kv ...string) string {
	vars := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		vars[kv[i]] = kv[i+1]
	}
	return v.App.I18n.TN(v.Lang, key, vars)
}

func (v *View) TraktEnabled() bool { return v.App.Sync.Trakt != nil }

func (v *View) TVDBEnabled() bool { return v.App.Sync.TVDBConfigured() }

func (v *View) TelegramBot() string {
	if v.App.Bot.Configured() {
		return v.App.Bot.Username
	}
	return ""
}

var genreSlug = regexp.MustCompile(`[^a-z0-9]+`)

func (v *View) Genre(name string) string {
	key := "genre." + strings.Trim(genreSlug.ReplaceAllString(strings.ToLower(name), "_"), "_")
	if v.App.I18n.HasKey(v.Lang, key) {
		return v.T(key)
	}
	return name
}

func (v *View) Genres(list string) []string {
	var out []string
	for _, g := range strings.Split(list, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, v.Genre(g))
		}
	}
	return out
}

func (v *View) T(key string, args ...any) string { return v.App.I18n.T(v.Lang, key, args...) }
func (v *View) AppName() string                  { return v.App.Cfg.AppName }
func (v *View) Langs() []string                  { return v.App.I18n.Langs() }

func (a *App) lang(w http.ResponseWriter, r *http.Request, u *store.User) string {
	if u != nil && a.I18n.Has(u.Locale) {
		return u.Locale
	}
	return a.I18n.Pick(r.Header.Get("Accept-Language"))
}

func (a *App) loadTemplates() error {
	funcs := template.FuncMap{
		"img":      imgURL,
		"imgset":   imgSet,
		"loveHref": loveHref,
		"starsOf":  starsOf,
		"code":     episodeCode,
		"add":      func(x, y int) int { return x + y },
		"seq":      seq,
		"pct":      pct,
		"year":     year,
		"aired":    aired,
		"stars":    func(f float64) string { return fmt.Sprintf("%.1f", f) },
		"dict":     dict,
		"hours":    fmtMinutes,
		"truncate": truncate,
		"minutes":  fmtMinutes,
		"join":     strings.Join,
		"initial":  initial,
		"avatar":   func(name string) string { return a.mediaURL("avatars", name) },
		"banner":   func(name string) string { return a.mediaURL("banners", name) },
		"assetV":   func() string { return assetVersion },
		"logoURL":  a.logoURL,
	}
	sub, err := fs.Sub(assets.FS, "templates")
	if err != nil {
		return fmt.Errorf("templates fs: %w", err)
	}
	pages, err := fs.Glob(sub, "*.html")
	if err != nil {
		return fmt.Errorf("glob templates: %w", err)
	}
	a.tmpl = map[string]*template.Template{}
	for _, p := range pages {
		if p == "layout.html" {
			continue
		}
		name := strings.TrimSuffix(p, ".html")
		t, err := template.New(name).Funcs(funcs).ParseFS(sub, "layout.html", "partials/*.html", p)
		if err != nil {
			return fmt.Errorf("parse template %s: %w", p, err)
		}
		a.tmpl[name] = t
	}
	return nil
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, status int, title string, data map[string]any) {
	t, ok := a.tmpl[name]
	if !ok {
		logger.Error("RENDER unknown template %q path=%s", name, r.URL.Path)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	u := auth.UserFrom(r.Context())
	v := &View{App: a, Lang: a.lang(w, r, u), User: u, CSRF: a.Auth.CSRFToken(w, r), Title: title, Path: r.URL.Path, Data: data}
	if u != nil {
		if n, err := a.Store.UnreadCount(u.ID); err != nil {
			logger.Error("RENDER unread count uid=%d: %v", u.ID, err)
		} else {
			v.Unread = n
		}
		if n, err := a.Store.PendingFriendCount(u.ID); err != nil {
			logger.Error("RENDER friend requests uid=%d: %v", u.ID, err)
		} else {
			v.Friends = n
		}
	}
	if u != nil && u.IsAdmin {
		if reports, servers, err := a.adminBadges(); err != nil {
			logger.Error("RENDER admin badges uid=%d: %v", u.ID, err)
		} else {
			v.AdminOpen = reports + servers
		}
	}
	if data == nil {
		v.Data = map[string]any{}
	}
	var buf strings.Builder
	if err := t.ExecuteTemplate(&buf, "layout", v); err != nil {
		logger.Error("RENDER template=%s path=%s: %v", name, r.URL.Path, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := io.WriteString(w, buf.String()); err != nil {
		logger.Warn("RENDER write path=%s: %v", r.URL.Path, err)
	}
}

func (a *App) page(w http.ResponseWriter, r *http.Request, name string, titleKey string, data map[string]any) {
	u := auth.UserFrom(r.Context())
	a.render(w, r, name, http.StatusOK, a.I18n.T(a.lang(w, r, u), titleKey), data)
}

func (a *App) errorPage(w http.ResponseWriter, r *http.Request, status int, key string) {
	u := auth.UserFrom(r.Context())
	lang := a.lang(w, r, u)
	a.render(w, r, "message", status, a.I18n.T(lang, "err.title"), map[string]any{"MessageKey": key, "Status": status})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Warn("JSON write: %v", err)
	}
}

type apiErr struct {
	status int
	key    string
}

func (e apiErr) Error() string { return e.key }

var (
	errBadRequest    = apiErr{http.StatusBadRequest, "err.bad_request"}
	errUnauth        = apiErr{http.StatusUnauthorized, "err.login_required"}
	errForbidden     = apiErr{http.StatusForbidden, "err.forbidden"}
	errNotFound      = apiErr{http.StatusNotFound, "err.not_found"}
	errRate          = apiErr{http.StatusTooManyRequests, "err.rate_limited"}
	errNeedVerify    = apiErr{http.StatusForbidden, "err.verify_email"}
	errMailDown      = apiErr{http.StatusServiceUnavailable, "err.mail_unavailable"}
	errTMDBDown      = apiErr{http.StatusBadGateway, "err.metadata_unavailable"}
	errInternal      = apiErr{http.StatusInternalServerError, "err.internal"}
	errTMDBMissing   = apiErr{http.StatusServiceUnavailable, "err.metadata_not_configured"}
	errSignupsClosed = apiErr{http.StatusForbidden, "err.signups_closed"}
	errReadOnly      = apiErr{http.StatusForbidden, "err.read_only"}
)

// fail logs the real cause server-side and returns only an i18n message to the
// client. apiErr values are expected outcomes; anything else is an internal
// error and is logged with the request for diagnosis.
func (a *App) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae apiErr
	if !errors.As(err, &ae) {
		logger.Error("API %s %s: %v", r.Method, r.URL.Path, err)
		ae = errInternal
	}
	u := auth.UserFrom(r.Context())
	msg := a.I18n.T(a.lang(w, r, u), ae.key)
	writeJSON(w, ae.status, map[string]any{"ok": false, "error": msg, "key": ae.key})
}

func (a *App) okJSON(w http.ResponseWriter, extra map[string]any) {
	out := map[string]any{"ok": true}
	for k, v := range extra {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10))
	if err := dec.Decode(dst); err != nil {
		return errBadRequest
	}
	return nil
}

func isStatic(p string) bool {
	return strings.HasPrefix(p, "/static/") || strings.HasPrefix(p, "/media/") || strings.HasPrefix(p, "/img/") || p == "/sw.js" || p == "/manifest.webmanifest" || p == "/healthz"
}

var publicPages = map[string]bool{
	"/login": true, "/login/2fa": true, "/register": true, "/forgot": true, "/privacy": true, "/about": true,
	"/offline": true, "/auth/telegram": true,
}

// publicRoute lists everything reachable without signing in: the sign-in and
// recovery pages, the legal pages, static files and the machine endpoints.
// Everything else needs an account.
func (a *App) publicRoute(p string) bool {
	return isStatic(p) || publicPages[p] || p == "/" || p == "/welcome" || p == "/api/contact" || strings.HasPrefix(p, "/api/captcha/") || a.isContactSlug(p) || a.isPageSlug(p) || strings.HasPrefix(p, "/t/") || strings.HasPrefix(p, "/api/auth/") || p == "/api/telegram/webhook" ||
		p == "/.well-known/tvt" || strings.HasPrefix(p, "/federation/")
}

// SetMedia swaps the object store (used by tests).
func (a *App) SetMedia(m media.Store) { a.Media = m }

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	a.routes(mux)
	return a.middleware(mux)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusWriter) WriteHeader(c int) {
	s.status = c
	s.ResponseWriter.WriteHeader(c)
}

func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: a.Auth.WrapResponse(w, r), status: 200}
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("PANIC %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				if sw.status == 200 {
					http.Error(sw, "internal error", http.StatusInternalServerError)
				}
			}
			if !isStatic(r.URL.Path) {
				logger.Info("HTTP %s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
			}
		}()
		sw.Header().Set("X-Content-Type-Options", "nosniff")
		sw.Header().Set("X-Frame-Options", "DENY")
		sw.Header().Set("Referrer-Policy", "same-origin")
		sw.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		if a.Cfg.CookieSecure {
			sw.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		sw.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:"+a.publicMediaOrigin()+"; style-src 'self'; script-src 'self' https://telegram.org; frame-src https://oauth.telegram.org https://telegram.org; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		ctx := r.Context()
		if !isStatic(r.URL.Path) {
			u, err := a.Auth.Authenticate(sw, r)
			switch {
			case err == nil:
				ctx = auth.WithUser(ctx, u)
			case errors.Is(err, auth.ErrBadToken), errors.Is(err, auth.ErrDeactivated):
				if errors.Is(err, auth.ErrDeactivated) {
					a.Auth.ClearCookies(sw)
				}
			default:
				logger.Error("AUTH authenticate %s %s: %v", r.Method, r.URL.Path, err)
			}
		}
		r = r.WithContext(ctx)
		if auth.UserFrom(ctx) == nil && !a.publicRoute(r.URL.Path) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				a.fail(sw, r, errUnauth)
				return
			}
			target := "/login"
			if r.Method == http.MethodGet && r.URL.Path != "/" {
				target += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(sw, r, target, http.StatusSeeOther)
			return
		}
		if !isStatic(r.URL.Path) && r.URL.Path != "/api/telegram/webhook" && !isFederation(r.URL.Path) {
			if err := a.allow(a.pageLimit, "g:"+auth.ClientIP(r)); err != nil {
				logger.Warn("RATE page limit from=%s %s %s", auth.ClientCountry(r), r.Method, r.URL.Path)
				a.fail(sw, r, err)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				key := "w:" + auth.ClientIP(r)
				if cu := auth.UserFrom(ctx); cu != nil {
					key = "w:u" + itoa(cu.ID)
				}
				if err := a.allow(a.writeLimit, key); err != nil {
					logger.Warn("RATE write limit from=%s %s %s", auth.ClientCountry(r), r.Method, r.URL.Path)
					a.fail(sw, r, err)
					return
				}
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && r.URL.Path != "/api/telegram/webhook" && !isFederation(r.URL.Path) {
			if !a.Auth.CSRFValid(r) {
				logger.Warn("CSRF rejected %s %s from=%s", r.Method, r.URL.Path, auth.ClientCountry(r))
				a.fail(sw, r, errForbidden)
				return
			}
		}
		next.ServeHTTP(sw, r)
	})
}

func (a *App) needUser(r *http.Request) (*store.User, error) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		return nil, errUnauth
	}
	return u, nil
}

func (a *App) needVerified(r *http.Request) (*store.User, error) {
	u, err := a.needUser(r)
	if err != nil {
		return nil, err
	}
	if u.IsBanned {
		return nil, errReadOnly
	}
	if !u.CanComment() {
		return nil, errNeedVerify
	}
	return u, nil
}

func (a *App) needAdmin(r *http.Request) (*store.User, error) {
	u, err := a.needUser(r)
	if err != nil {
		return nil, err
	}
	if !u.IsAdmin {
		return nil, errForbidden
	}
	return u, nil
}

func (a *App) allow(l *auth.Limiter, key string) error {
	if !l.Allow(key) {
		return errRate
	}
	return nil
}

func withTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

func (v *View) JSStrings() map[string]string { return v.App.I18n.Prefix(v.Lang, "js.") }

func isFederation(p string) bool {
	return p == "/.well-known/tvt" || strings.HasPrefix(p, "/federation/")
}
