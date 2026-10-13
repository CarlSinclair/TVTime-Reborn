package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tvt/auth"
	"tvt/logger"
	"tvt/notify"
	"tvt/store"
)

type notifView struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Link      string `json:"link"`
	Read      bool   `json:"read"`
	CreatedAt string `json:"created_at"`
	Open      string `json:"open"`
}

func (a *App) notifViews(lang string, list []store.Notification) []notifView {
	out := make([]notifView, 0, len(list))
	for _, n := range list {
		title, body := a.Notif.Text(lang, n.Event, n.Data)
		out = append(out, notifView{ID: n.ID, Title: title, Body: body, Link: n.Link, Read: n.Read, CreatedAt: n.CreatedAt, Open: "/notifications/" + itoa(n.ID) + "/open"})
	}
	return out
}

func (a *App) apiNotificationsRecent(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	list, err := a.Store.Notifications(u.ID, 8, 0)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	unread, err := a.Store.UnreadCount(u.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"unread": unread, "items": a.notifViews(a.lang(w, r, u), list)})
}

func (a *App) pageNotifications(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	const per = 20
	list, err := a.Store.Notifications(u.ID, per, (page-1)*per)
	if err != nil {
		logger.Error("PAGE notifications uid=%d: %v", u.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	total, err := a.Store.CountNotifications(u.ID)
	if err != nil {
		logger.Error("PAGE notifications count uid=%d: %v", u.ID, err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	views := a.notifViews(a.lang(w, r, u), list)
	if err := a.Store.MarkAllNotificationsRead(u.ID); err != nil {
		logger.Error("PAGE notifications mark read uid=%d: %v", u.ID, err)
	}
	a.page(w, r, "notifications", "nav.notifications", map[string]any{
		"Items": views, "Page": page, "HasPrev": page > 1, "HasNext": page*per < total, "Prev": page - 1, "Next": page + 1,
	})
}

func safeLocalPath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.Contains(p, "\\")
}

func (a *App) pageNotificationOpen(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/notifications", http.StatusSeeOther)
		return
	}
	n, err := a.Store.NotificationByID(u.ID, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			logger.Error("PAGE notification open id=%d uid=%d: %v", id, u.ID, err)
		}
		http.Redirect(w, r, "/notifications", http.StatusSeeOther)
		return
	}
	if err := a.Store.MarkNotificationRead(u.ID, id); err != nil {
		logger.Error("PAGE notification mark read id=%d uid=%d: %v", id, u.ID, err)
	}
	target := "/notifications"
	if safeLocalPath(n.Link) {
		target = n.Link
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *App) apiNotificationRead(w http.ResponseWriter, r *http.Request) {
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
	if err := a.Store.MarkNotificationRead(u.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	unread, _ := a.Store.UnreadCount(u.ID)
	a.okJSON(w, map[string]any{"unread": unread})
}

func (a *App) apiNotificationsReadAll(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Store.MarkAllNotificationsRead(u.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"unread": 0})
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

var pushHosts = []string{".googleapis.com", ".push.services.mozilla.com", ".push.apple.com", ".notify.windows.com", ".push.microsoft.com"}

// validPushEndpoint limits server-side requests to the browser vendors' push
// services, so a subscription cannot be used to make the server call an
// arbitrary URL.
func validPushEndpoint(raw string) bool {
	if len(raw) > 2048 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suf := range pushHosts {
		if strings.HasSuffix(host, suf) || host == strings.TrimPrefix(suf, ".") {
			return true
		}
	}
	return false
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
	if err := decode(r, &in); err != nil || !validPushEndpoint(in.Endpoint) {
		a.fail(w, r, errBadRequest)
		return
	}
	if err := a.Store.SavePushSub(u.ID, in.Endpoint, in.Keys.P256dh, in.Keys.Auth, trunc(r.UserAgent(), 200)); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
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
