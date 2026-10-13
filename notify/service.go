package notify

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"sync"
	"time"

	"tvt/i18n"
	"tvt/logger"
	"tvt/mailer"
	"tvt/store"
	"tvt/telegram"
)

const (
	ChanInApp    = "in_app"
	ChanPush     = "push"
	ChanEmail    = "email"
	ChanTelegram = "telegram"
)

var Channels = []string{ChanInApp, ChanPush, ChanEmail, ChanTelegram}

type Category struct {
	ID        string
	LabelKey  string
	Security  bool
	AdminOnly bool
	Default   store.ChannelPref
}

// Categories mirrors CTime's preference matrix: security alerts are forced on
// for in-app and email; everything else is opt-out per channel.
var Categories = []Category{
	{ID: "security", LabelKey: "notif.cat.security", Security: true, Default: store.ChannelPref{InApp: true, Push: true, Email: true, Telegram: true}},
	{ID: "episodes", LabelKey: "notif.cat.episodes", Default: store.ChannelPref{InApp: true, Push: true, Email: false, Telegram: true}},
	{ID: "social", LabelKey: "notif.cat.social", Default: store.ChannelPref{InApp: true, Push: true, Email: false, Telegram: false}},
	{ID: "moderation", LabelKey: "notif.cat.moderation", AdminOnly: true, Default: store.ChannelPref{InApp: true, Push: true, Email: true, Telegram: true}},
	{ID: "system", LabelKey: "notif.cat.system", Default: store.ChannelPref{InApp: true, Push: false, Email: false, Telegram: false}},
}

func CategoryByID(id string) (Category, bool) {
	for _, c := range Categories {
		if c.ID == id {
			return c, true
		}
	}
	return Category{}, false
}

type Event struct {
	Name     string
	Category string
}

var Events = map[string]Event{
	"episode_aired":          {"episode_aired", "episodes"},
	"comment_reply":          {"comment_reply", "social"},
	"password_changed":       {"password_changed", "security"},
	"twofa_enabled":          {"twofa_enabled", "security"},
	"twofa_disabled":         {"twofa_disabled", "security"},
	"telegram_linked":        {"telegram_linked", "security"},
	"passkey_added":          {"passkey_added", "security"},
	"telegram_dropped":       {"telegram_dropped", "system"},
	"abuse_report":           {"abuse_report", "moderation"},
	"import_done":            {"import_done", "system"},
	"email_change_requested": {"email_change_requested", "security"},
	"email_changed":          {"email_changed", "security"},
	"test":                   {"test", "system"},
}

var (
	ErrEmailUnavailable    = errors.New("no verified email address")
	ErrTelegramUnavailable = errors.New("telegram is not linked")
	ErrUnknownChannel      = errors.New("unknown channel")
)

type Service struct {
	Store   *store.Store
	Mail    *mailer.Mailer
	Bot     *telegram.Bot
	I18n    *i18n.Bundle
	Push    *Pusher
	Bus     *Bus
	AppName string
	BaseURL string
	Inline  bool

	mailQ chan mailJob
	wg    sync.WaitGroup

	dupMu sync.Mutex
	dups  map[string]time.Time
}

const duplicateWindow = 10 * time.Minute

// duplicate reports whether the same notification (same user, event and text)
// was already delivered inside the window, so something that keeps failing and
// reporting itself cannot flood a person with the same message.
func (s *Service) duplicate(userID int64, event, title, body, link string) bool {
	if event == "test" {
		return false
	}
	key := fmt.Sprintf("%d|%s|%s|%s|%s", userID, event, title, body, link)
	now := time.Now()
	s.dupMu.Lock()
	defer s.dupMu.Unlock()
	if s.dups == nil {
		s.dups = map[string]time.Time{}
	}
	if at, ok := s.dups[key]; ok && now.Sub(at) < duplicateWindow {
		return true
	}
	if len(s.dups) > 5000 {
		for k, at := range s.dups {
			if now.Sub(at) >= duplicateWindow {
				delete(s.dups, k)
			}
		}
	}
	s.dups[key] = now
	return false
}

type mailJob struct {
	to, subject, text, html string
	attempt                 int
	userID                  int64
}

func New(st *store.Store, ml *mailer.Mailer, bot *telegram.Bot, bundle *i18n.Bundle, push *Pusher, appName, baseURL string) *Service {
	return &Service{Store: st, Mail: ml, Bot: bot, I18n: bundle, Push: push, Bus: NewBus(), AppName: appName, BaseURL: strings.TrimRight(baseURL, "/"), mailQ: make(chan mailJob, 512)}
}

// Start runs the email workers until ctx ends.
func (s *Service) Start(ctx context.Context) {
	for i := 0; i < 2; i++ {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-s.mailQ:
					s.deliverMail(ctx, job)
				}
			}
		}()
	}
}

func (s *Service) deliverMail(ctx context.Context, job mailJob) {
	if err := s.Mail.SendHTML(job.to, job.subject, job.text, job.html); err == nil {
		return
	} else {
		logger.Error("NOTIFY email failed uid=%d attempt=%d: %v", job.userID, job.attempt+1, err)
	}
	if job.attempt >= 2 {
		logger.Error("NOTIFY email gave up uid=%d subject=%q after %d attempts", job.userID, job.subject, job.attempt+1)
		return
	}
	job.attempt++
	delay := []time.Duration{20 * time.Second, 3 * time.Minute}[job.attempt-1]
	time.AfterFunc(delay, func() {
		select {
		case s.mailQ <- job:
		case <-ctx.Done():
		}
	})
}

func vars(data map[string]any) map[string]string {
	out := make(map[string]string, len(data))
	for k, v := range data {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// Text renders an event's title and body in a language; stored notifications
// keep only keys and data so each reader sees their own language.
func (s *Service) Text(lang, event string, data map[string]any) (title, body string) {
	v := vars(data)
	return s.I18n.TN(lang, "notif."+event+".title", v), s.I18n.TN(lang, "notif."+event+".body", v)
}

func (s *Service) prefFor(u *store.User, cat Category, prefs map[string]store.ChannelPref, channel string) bool {
	if cat.AdminOnly && !u.IsAdmin {
		return false
	}
	if cat.Security {
		return true
	}
	master := false
	switch channel {
	case ChanInApp:
		master = u.NotifyInApp
	case ChanPush:
		master = u.NotifyPush
	case ChanEmail:
		master = u.NotifyEmail
	case ChanTelegram:
		master = u.NotifyTelegram
	}
	if !master {
		return false
	}
	p, ok := prefs[cat.ID]
	if !ok {
		p = cat.Default
	}
	switch channel {
	case ChanInApp:
		return p.InApp
	case ChanPush:
		return p.Push
	case ChanEmail:
		return p.Email
	case ChanTelegram:
		return p.Telegram
	}
	return false
}

func (s *Service) canEmail(u *store.User) bool { return u.Email != "" && u.EmailVerified }
func (s *Service) canTelegram(u *store.User) bool {
	return s.Bot.Configured() && u.TelegramChatID != ""
}

// Send stores and delivers one event to one user across every channel the user
// has enabled. Delivery runs in the background unless Inline is set (tests);
// every channel failure is logged with the user and event.
func (s *Service) Send(userID int64, event, link string, data map[string]any) {
	if s.Inline {
		s.deliver(userID, event, link, data)
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("NOTIFY panic uid=%d event=%s: %v", userID, event, r)
			}
		}()
		s.deliver(userID, event, link, data)
	}()
}

func (s *Service) SendToAdmins(event, link string, data map[string]any) {
	admins, err := s.Store.Admins()
	if err != nil {
		logger.Error("NOTIFY list admins for event=%s: %v", event, err)
		return
	}
	for _, a := range admins {
		s.Send(a.ID, event, link, data)
	}
}

func (s *Service) deliver(userID int64, event, link string, data map[string]any) {
	ev, ok := Events[event]
	if !ok {
		logger.Error("NOTIFY unknown event %q uid=%d", event, userID)
		return
	}
	cat, _ := CategoryByID(ev.Category)
	u, err := s.Store.UserByID(userID)
	if err != nil {
		logger.Error("NOTIFY load user uid=%d event=%s: %v", userID, event, err)
		return
	}
	if u.IsBanned {
		return
	}
	prefs, err := s.Store.NotificationPrefs(userID)
	if err != nil {
		logger.Error("NOTIFY load prefs uid=%d event=%s: %v", userID, event, err)
		prefs = map[string]store.ChannelPref{}
	}
	if data == nil {
		data = map[string]any{}
	}
	lang := u.Locale
	if !s.I18n.Has(lang) {
		lang = "en"
	}
	title, body := s.Text(lang, event, data)
	if s.duplicate(userID, event, title, body, link) {
		logger.Warn("NOTIFY duplicate suppressed uid=%d event=%s", userID, event)
		return
	}
	abs := ""
	if link != "" {
		abs = s.BaseURL + link
	}

	unread := 0
	if s.prefFor(u, cat, prefs, ChanInApp) {
		id, err := s.Store.InsertNotification(&store.Notification{UserID: userID, Event: event, Category: ev.Category, TitleKey: "notif." + event + ".title", BodyKey: "notif." + event + ".body", Data: data, Link: link})
		if err != nil {
			logger.Error("NOTIFY in-app store failed uid=%d event=%s: %v", userID, event, err)
		} else {
			unread, err = s.Store.UnreadCount(userID)
			if err != nil {
				logger.Error("NOTIFY unread count uid=%d: %v", userID, err)
			}
			s.Bus.Publish(userID, BusMessage{ID: id, Title: title, Body: body, Link: link, Event: event, UnreadCount: unread})
		}
	}
	if s.prefFor(u, cat, prefs, ChanPush) {
		if _, err := s.Push.SendToUser(userID, title, body, link, unread); err != nil && !errors.Is(err, ErrNoPushDevices) {
			logger.Error("NOTIFY push failed uid=%d event=%s: %v", userID, event, err)
		}
	}
	if s.prefFor(u, cat, prefs, ChanEmail) && s.canEmail(u) {
		s.queueEmail(u, lang, title, body, abs)
	}
	if s.prefFor(u, cat, prefs, ChanTelegram) && s.canTelegram(u) {
		if err := s.sendTelegram(u, title, body, abs); err != nil {
			logger.Error("NOTIFY telegram failed uid=%d event=%s: %v", userID, event, err)
		}
	}
}

func (s *Service) sendTelegram(u *store.User, title, body, absLink string) error {
	msg := title
	if body != "" {
		msg += "\n" + body
	}
	if absLink != "" {
		msg += "\n\n" + absLink
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := s.Bot.SendMessage(ctx, u.TelegramChatID, msg)
	if errors.Is(err, telegram.ErrBotBlocked) {
		if cerr := s.Store.SetTelegramChat(u.ID, ""); cerr != nil {
			logger.Error("NOTIFY clear dead telegram chat uid=%d: %v", u.ID, cerr)
		}
		logger.Warn("NOTIFY telegram chat dropped uid=%d (bot blocked)", u.ID)
		s.Send(u.ID, "telegram_dropped", "/settings?tab=notifications", nil)
	}
	return err
}

var emailTmpl = template.Must(template.New("mail").Parse(`<!doctype html><html lang="{{.Lang}}" dir="{{.Dir}}"><body style="margin:0;background:#f3f4f8;font-family:system-ui,-apple-system,'Segoe UI',Roboto,sans-serif;color:#1b1e2b">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr><td align="center" style="padding:24px 12px">
<table role="presentation" width="100%" style="max-width:520px;background:#ffffff;border-radius:12px;border:1px solid #dcdfeb" cellpadding="0" cellspacing="0">
<tr><td style="background:#7c5cff;border-radius:12px 12px 0 0;padding:16px 24px;color:#ffffff;font-weight:800;font-size:18px">{{.App}}</td></tr>
<tr><td style="padding:24px"><h1 style="margin:0 0 12px;font-size:20px;line-height:1.3">{{.Title}}</h1>
{{if .Body}}<p style="margin:0 0 20px;font-size:16px;line-height:1.5;white-space:pre-line">{{.Body}}</p>{{end}}
{{if .Link}}<p style="margin:0 0 8px"><a href="{{.Link}}" style="display:inline-block;background:#7c5cff;color:#ffffff;text-decoration:none;font-weight:700;padding:12px 22px;border-radius:8px">{{.Button}}</a></p>{{end}}
</td></tr>
<tr><td style="padding:16px 24px;border-top:1px solid #dcdfeb;color:#666b84;font-size:12px">{{.Footer}}</td></tr>
</table></td></tr></table></body></html>`))

func isRTL(lang string) bool { return lang == "ar" }

func (s *Service) renderEmail(lang, title, body, absLink string) (text, htmlBody string, err error) {
	dir := "ltr"
	if isRTL(lang) {
		dir = "rtl"
	}
	button := s.I18n.T(lang, "mail.button_open")
	footer := s.I18n.TN(lang, "mail.footer", map[string]string{"app": s.AppName})
	var sb strings.Builder
	if err := emailTmpl.Execute(&sb, map[string]any{"Lang": lang, "Dir": dir, "App": s.AppName, "Title": title, "Body": body, "Link": absLink, "Button": button, "Footer": footer}); err != nil {
		return "", "", fmt.Errorf("render notification email: %w", err)
	}
	text = title
	if body != "" {
		text += "\n\n" + body
	}
	if absLink != "" {
		text += "\n\n" + absLink
	}
	text += "\n\n-- " + footer
	return text, sb.String(), nil
}

func (s *Service) queueEmail(u *store.User, lang, title, body, absLink string) {
	text, htmlBody, err := s.renderEmail(lang, title, body, absLink)
	if err != nil {
		logger.Error("NOTIFY email render uid=%d: %v", u.ID, err)
		return
	}
	job := mailJob{to: u.Email, subject: title, text: text, html: htmlBody, userID: u.ID}
	if s.Inline {
		s.deliverMail(context.Background(), job)
		return
	}
	select {
	case s.mailQ <- job:
	default:
		logger.Error("NOTIFY email queue full, dropping uid=%d subject=%q", u.ID, title)
	}
}

// SendTest pushes a test message down exactly one channel, ignoring
// preferences, and returns the real delivery error so the settings UI can show
// it.
func (s *Service) SendTest(u *store.User, channel string) error {
	lang := u.Locale
	if !s.I18n.Has(lang) {
		lang = "en"
	}
	title, body := s.Text(lang, "test", map[string]any{})
	link := "/settings?tab=notifications"
	switch channel {
	case ChanInApp:
		id, err := s.Store.InsertNotification(&store.Notification{UserID: u.ID, Event: "test", Category: "system", TitleKey: "notif.test.title", BodyKey: "notif.test.body", Data: map[string]any{}, Link: link})
		if err != nil {
			return err
		}
		unread, _ := s.Store.UnreadCount(u.ID)
		s.Bus.Publish(u.ID, BusMessage{ID: id, Title: title, Body: body, Link: link, Event: "test", UnreadCount: unread})
		return nil
	case ChanPush:
		unread, _ := s.Store.UnreadCount(u.ID)
		_, err := s.Push.SendTestToUser(u.ID, title, body, link, unread)
		return err
	case ChanEmail:
		if !s.canEmail(u) {
			return ErrEmailUnavailable
		}
		text, htmlBody, err := s.renderEmail(lang, title, body, s.BaseURL+link)
		if err != nil {
			return err
		}
		return s.Mail.SendHTML(u.Email, title, text, htmlBody)
	case ChanTelegram:
		if !s.canTelegram(u) {
			return ErrTelegramUnavailable
		}
		return s.sendTelegram(u, title, body, s.BaseURL+link)
	}
	return ErrUnknownChannel
}
