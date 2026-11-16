package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tvt/auth"
	"tvt/logger"
	"tvt/notify"
	"tvt/store"

	"github.com/CarlSinclair/csuit-libs/notifykit"
)

type notifStore struct {
	app    *App
	lang   string
	userID int64
}

func (s notifStore) Counts(userID int64) ([]notifykit.CategoryCount, error) {
	rows, err := s.app.Store.NotificationCounts(userID)
	if err != nil {
		return nil, err
	}
	out := make([]notifykit.CategoryCount, 0, len(rows))
	for _, r := range rows {
		cc := notifykit.CategoryCount{Category: r.Category, Total: r.Total, Unread: r.Unread}
		latest, _, lerr := s.app.Store.NotificationsFiltered(userID, r.Category, false, 1, 0)
		if lerr != nil {
			logger.Warn("notification latest lookup uid=%d category=%s: %v", userID, r.Category, lerr)
		} else if len(latest) == 1 {
			cc.LatestTitle, _ = s.app.Notif.Text(s.lang, latest[0].Event, latest[0].Data)
			cc.LatestAt = parseNotifTime(latest[0].CreatedAt)
		}
		out = append(out, cc)
	}
	return out, nil
}

func (s notifStore) item(n store.Notification) notifykit.Item {
	title, body := s.app.Notif.Text(s.lang, n.Event, n.Data)
	it := notifykit.Item{ID: n.ID, Event: n.Event, Category: n.Category, Title: title, Body: body, Link: n.Link, Read: n.Read, CreatedAt: parseNotifTime(n.CreatedAt)}
	if n.Event == "friend_request" {
		if name, _ := n.Data["username"].(string); name != "" {
			if viewer, err := s.app.Store.UserByID(s.userID); err == nil {
				if p, err := s.app.Store.UserByUsername(name); err == nil {
					if state, err := s.app.Store.FriendState(viewer.ID, p.ID); err == nil && state == "incoming" {
						it.Extra = map[string]string{"friend": p.Username}
					}
				}
			}
		}
	}
	return it
}

func (s notifStore) List(userID int64, f notifykit.Filter) ([]notifykit.Item, int, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = notifykit.PageSize
	}
	list, total, err := s.app.Store.NotificationsFiltered(userID, f.Category, f.UnreadOnly, limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	out := make([]notifykit.Item, 0, len(list))
	for _, n := range list {
		out = append(out, s.item(n))
	}
	return out, total, nil
}

func (s notifStore) Get(userID, id int64) (notifykit.Item, error) {
	n, err := s.app.Store.NotificationByID(userID, id)
	if err != nil {
		return notifykit.Item{}, err
	}
	return s.item(*n), nil
}

func (s notifStore) MarkRead(userID, id int64) error {
	return s.app.Store.MarkNotificationRead(userID, id)
}

func (s notifStore) MarkCategoryRead(userID int64, category string) (int64, error) {
	return s.app.Store.MarkCategoryRead(userID, category)
}

func parseNotifTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (a *App) notifykitHandler(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	lang := a.lang(w, r, u)
	notifykit.New(notifykit.Config{
		Store:    notifStore{app: a, lang: lang, userID: notifUserID(u)},
		Prefix:   "/notifications",
		Fallback: "/",
		UserID: func(r *http.Request) (int64, bool) {
			u := auth.UserFrom(r.Context())
			if u == nil {
				return 0, false
			}
			return u.ID, true
		},
	}).ServeHTTP(w, r)
}

func (v *View) NotifBell() template.HTML {
	cats := map[string]string{}
	for _, c := range []string{notifykit.CatSecurity, notifykit.CatEpisodes, notifykit.CatSocial, notifykit.CatModeration, notifykit.CatSystem, notifykit.CatOther} {
		cats[c] = v.T("notif.cat." + c)
	}
	return template.HTML(notifykit.BellHTML("/notifications", v.CSRF, "/static/icons.svg?v="+assetVersion+"#fa-bell", notifykit.Labels{
		Title:      v.T("nav.notifications"),
		Empty:      v.T("notif.empty"),
		History:    v.T("notif.history"),
		Back:       v.T("notif.back"),
		MarkGroup:  v.T("notif.mark_group"),
		Prev:       v.T("notif.prev"),
		Next:       v.T("notif.next"),
		Close:      v.T("notif.close"),
		Error:      v.T("notif.load_failed"),
		Categories: cats,
	}))
}

// apiNotificationsStream delivers notifications to open tabs over SSE with no
// polling: it blocks on the in-process bus and writes only when something
// arrives; the heartbeat just keeps proxies from timing the connection out.
func (a *App) apiNotificationsStream(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		logger.Error("SSE response writer cannot flush uid=%d", u.ID)
		a.fail(w, r, errInternal)
		return
	}
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		logger.Warn("SSE could not clear write deadline uid=%d: %v", u.ID, err)
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	events, unsub := a.Notif.Bus.Subscribe(u.ID)
	defer unsub()
	fmt.Fprint(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()
	beat := time.NewTicker(20 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				logger.Error("SSE marshal uid=%d: %v", u.ID, err)
				continue
			}
			if _, err := fmt.Fprintf(w, "event: notification\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (a *App) apiNotificationSettings(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Master map[string]bool            `json:"master"`
		Prefs  map[string]map[string]bool `json:"prefs"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.Master[notify.ChanTelegram] && u.TelegramChatID == "" {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.telegram_not_linked"})
		return
	}
	if in.Master[notify.ChanEmail] && !(u.Email != "" && u.EmailVerified) {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.email_not_verified"})
		return
	}
	if in.Master[notify.ChanPush] {
		subs, err := a.Store.PushSubs(u.ID)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if len(subs) == 0 {
			a.fail(w, r, apiErr{http.StatusBadRequest, "err.push_not_enabled"})
			return
		}
	}
	if in.Master[notify.ChanTelegram] && !u.NotifyTelegram {
		if err := a.confirmTelegramOn(r.Context(), u); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	if err := a.Store.SetNotifyFlags(u.ID, in.Master[notify.ChanInApp], in.Master[notify.ChanPush], in.Master[notify.ChanEmail], in.Master[notify.ChanTelegram]); err != nil {
		a.fail(w, r, err)
		return
	}
	prefs := map[string]store.ChannelPref{}
	for _, cat := range notify.Categories {
		if cat.AdminOnly && !u.IsAdmin {
			continue
		}
		p := in.Prefs[cat.ID]
		cp := store.ChannelPref{InApp: p[notify.ChanInApp], Push: p[notify.ChanPush], Email: p[notify.ChanEmail], Telegram: p[notify.ChanTelegram]}
		if cat.Security {
			cp.InApp, cp.Email = true, true
		}
		prefs[cat.ID] = cp
	}
	if err := a.Store.SaveNotificationPrefs(u.ID, prefs); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

func (a *App) apiNotificationTest(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Channel string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.mailLimit, "ntest:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	switch err := a.Notif.SendTest(u, in.Channel); {
	case err == nil:
		a.okJSON(w, nil)
	case errors.Is(err, notify.ErrEmailUnavailable):
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.notify_no_email"})
	case errors.Is(err, notify.ErrTelegramUnavailable):
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.notify_no_telegram"})
	case errors.Is(err, notify.ErrNoPushDevices):
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.notify_no_push"})
	case errors.Is(err, notify.ErrUnknownChannel):
		a.fail(w, r, errBadRequest)
	default:
		logger.Error("NOTIFY test channel=%s uid=%d: %v", in.Channel, u.ID, err)
		a.fail(w, r, apiErr{http.StatusBadGateway, "err.notify_test_failed"})
	}
}

func (a *App) apiPushKey(w http.ResponseWriter, r *http.Request) {
	if _, err := a.needUser(r); err != nil {
		a.fail(w, r, err)
		return
	}
	key, err := a.Notif.Push.PublicKey()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"key": key})
}

func (a *App) apiPushSubscribe(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := decode(r, &in); err != nil || !notify.ValidBrowserEndpoint(in.Endpoint) {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.Store.SavePushSub(u.ID, in.Endpoint, in.Keys.P256dh, in.Keys.Auth, trunc(r.UserAgent(), 200)); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

func (a *App) apiPushDevice(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := decode(r, &in); err != nil || !notify.ValidDeviceEndpoint(in.Endpoint) || in.Keys.P256dh == "" || in.Keys.Auth == "" {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.Store.SavePushSub(u.ID, in.Endpoint, in.Keys.P256dh, in.Keys.Auth, "app: "+trunc(r.UserAgent(), 190)); err != nil {
		logger.Error("PUSH device register uid=%d host=%s: %v", u.ID, hostOfURL(in.Endpoint), err)
		a.fail(w, r, err)
		return
	}
	logger.Info("PUSH device registered uid=%d host=%s", u.ID, hostOfURL(in.Endpoint))
	a.okJSON(w, nil)
}

func hostOfURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "?"
	}
	return u.Host
}

func (a *App) apiPushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Endpoint string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Store.DeletePushSub(u.ID, in.Endpoint); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (a *App) apiNotificationTiming(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		OffsetMin int `json:"offset_min"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.OffsetMin < -store.MaxNotifyOffsetMin || in.OffsetMin > store.MaxNotifyOffsetMin {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.invalid_timing"})
		return
	}
	if err := a.Store.SetNotifyTiming(u.ID, in.OffsetMin); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

func notifUserID(u *store.User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}
