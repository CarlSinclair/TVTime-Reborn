package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tvt/auth"
	"tvt/logger"
	"tvt/store"
	"tvt/telegram"
)

func hashLinkToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func tgLangFor(a *App, code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return a.Cfg.DefaultLang
	}
	return a.I18n.Pick(code)
}

// pageTelegramLogin handles the Telegram Login Widget redirect. A signed-in
// user with ?action=link attaches the Telegram identity to their account;
// otherwise it signs in the linked account, creating one on first use.
func (a *App) pageTelegramLogin(w http.ResponseWriter, r *http.Request) {
	fail := func(code string) { http.Redirect(w, r, "/login?error="+code, http.StatusSeeOther) }
	if !a.Bot.Configured() {
		fail("telegram_unavailable")
		return
	}
	if err := a.allow(a.authLimit, "tg:"+auth.ClientIP(r)); err != nil {
		fail("rate_limited")
		return
	}
	data, err := a.Bot.VerifyLogin(r.URL.Query())
	if err != nil {
		logger.Warn("AUTH telegram login rejected from=%s: %v", auth.ClientCountry(r), err)
		fail("telegram_failed")
		return
	}
	current := auth.UserFrom(r.Context())

	if r.URL.Query().Get("action") == "link" {
		if current == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		switch err := a.Store.LinkTelegram(current.ID, data.ID, data.Username); {
		case err == nil:
			a.Notif.Send(current.ID, "telegram_linked", "/settings?tab=account", nil)
			http.Redirect(w, r, "/settings?tab=account&linked=1", http.StatusSeeOther)
		case errors.Is(err, store.ErrTelegramTaken):
			http.Redirect(w, r, "/settings?tab=account&error=telegram_taken", http.StatusSeeOther)
		default:
			logger.Error("AUTH telegram link uid=%d tg=%d: %v", current.ID, data.ID, err)
			http.Redirect(w, r, "/settings?tab=account&error=internal", http.StatusSeeOther)
		}
		return
	}

	u, err := a.Store.UserByTelegramID(data.ID)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		if a.signupsClosed() {
			fail("signups_closed")
			return
		}
		u, err = a.Store.CreateTelegramUser(data.ID, data.Username, data.DisplayName(), a.lang(w, r, nil))
		if err != nil {
			logger.Error("AUTH telegram register tg=%d: %v", data.ID, err)
			fail("internal")
			return
		}
		logger.Info("AUTH telegram registered uid=%d tg=%d", u.ID, data.ID)
	default:
		logger.Error("AUTH telegram lookup tg=%d: %v", data.ID, err)
		fail("internal")
		return
	}
	if u.IsBanned {
		fail("banned")
		return
	}
	if data.Username != u.TelegramUsername {
		if err := a.Store.LinkTelegram(u.ID, data.ID, data.Username); err != nil {
			logger.Warn("AUTH telegram refresh username uid=%d: %v", u.ID, err)
		}
	}
	a.finishLoginRedirect(w, r, u)
}

func (a *App) apiTelegramLinkToken(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !a.Bot.Configured() {
		a.fail(w, r, apiErr{http.StatusServiceUnavailable, "err.telegram_unavailable"})
		return
	}
	if err := a.allow(a.mailLimit, "tglink:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		a.fail(w, r, err)
		return
	}
	token := hex.EncodeToString(b)
	if err := a.Store.CreateTelegramLinkToken(u.ID, hashLinkToken(token), time.Now().Add(10*time.Minute)); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"url": a.Bot.DeepLink(token)})
}

func (a *App) apiTelegramStatus(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	fresh, err := a.Store.UserByID(u.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"chat_linked": fresh.TelegramChatID != "", "identity_linked": fresh.TelegramID != 0, "username": fresh.TelegramUsername})
}

func (a *App) apiTelegramUnlinkChat(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Store.SetTelegramChat(u.ID, ""); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

// apiTelegramUnlinkIdentity removes Telegram as a login method. It refuses
// when that would leave the account with no way to sign in.
func (a *App) apiTelegramUnlinkIdentity(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !u.HasPassword() && !(u.Email != "" && u.EmailVerified) {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.last_login_method"})
		return
	}
	if err := a.Store.UnlinkTelegram(u.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

// apiTelegramEnableDM connects notifications for accounts that signed in with
// Telegram: their numeric id is also their private chat id with the bot, but
// only if they have started it, so a real message is sent and its result
// decides success.
func (a *App) apiTelegramEnableDM(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !a.Bot.Configured() {
		a.fail(w, r, apiErr{http.StatusServiceUnavailable, "err.telegram_unavailable"})
		return
	}
	if u.TelegramID == 0 {
		a.fail(w, r, errBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	title, body := a.Notif.Text(u.Locale, "test", map[string]any{})
	if err := a.Bot.SendMessage(ctx, itoa(u.TelegramID), title+"\n"+body); err != nil {
		if errors.Is(err, telegram.ErrBotBlocked) {
			a.fail(w, r, apiErr{http.StatusBadRequest, "err.telegram_start_bot"})
		} else {
			logger.Error("TELEGRAM enable dm uid=%d: %v", u.ID, err)
			a.fail(w, r, apiErr{http.StatusBadGateway, "err.telegram_send_failed"})
		}
		return
	}
	if err := a.Store.SetTelegramChatClearOthers(u.ID, itoa(u.TelegramID)); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

// handleTelegramWebhook receives bot updates: "/start <token>" links a private
// chat to an account for notifications; replies are in the user's language.
func (a *App) handleTelegramWebhook(w http.ResponseWriter, r *http.Request) {
	if !a.Bot.VerifyWebhook(r) {
		logger.Warn("TELEGRAM webhook rejected from=%s", auth.ClientCountry(r))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var upd telegram.Update
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&upd); err != nil {
		logger.Error("TELEGRAM webhook decode: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	msg := upd.Message
	if msg == nil {
		return
	}
	cmd, arg := telegram.ParseCommand(msg.Text)
	if cmd == "" {
		return
	}
	chat := strconv.FormatInt(msg.Chat.ID, 10)
	hint := ""
	if msg.From != nil {
		hint = msg.From.LanguageCode
	}
	reply := func(lang, key string) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.Bot.SendMessage(ctx, chat, a.I18n.T(lang, key)); err != nil {
			logger.Error("TELEGRAM reply chat=%s key=%s: %v", chat, key, err)
		}
	}
	lang := tgLangFor(a, hint)
	if msg.Chat.Type != "private" {
		reply(lang, "tg.private_only")
		return
	}
	existing, lookupErr := a.Store.UserByTelegramChat(chat)
	if lookupErr == nil {
		lang = existing.Locale
	} else if !errors.Is(lookupErr, store.ErrNotFound) {
		logger.Error("TELEGRAM lookup chat=%s: %v", chat, lookupErr)
		reply(lang, "tg.error")
		return
	}
	switch cmd {
	case "/start":
		if arg == "" {
			if lookupErr == nil {
				reply(lang, "tg.status_linked")
			} else {
				reply(lang, "tg.status_unlinked")
			}
			return
		}
		uid, err := a.Store.ConsumeTelegramLinkToken(hashLinkToken(arg))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				reply(lang, "tg.link_invalid")
			} else {
				logger.Error("TELEGRAM consume link token chat=%s: %v", chat, err)
				reply(lang, "tg.error")
			}
			return
		}
		if err := a.Store.SetTelegramChatClearOthers(uid, chat); err != nil {
			logger.Error("TELEGRAM link uid=%d chat=%s: %v", uid, chat, err)
			reply(lang, "tg.error")
			return
		}
		if u, err := a.Store.UserByID(uid); err == nil {
			lang = u.Locale
			if u.TelegramID == 0 && msg.From != nil && msg.From.ID != 0 {
				switch err := a.Store.LinkTelegram(uid, msg.From.ID, msg.From.Username); {
				case err == nil:
					logger.Info("TELEGRAM sign-in identity linked with the chat uid=%d", uid)
				case errors.Is(err, store.ErrTelegramTaken):
					logger.Warn("TELEGRAM chat linked but the Telegram account already belongs to another user uid=%d", uid)
				default:
					logger.Error("TELEGRAM link sign-in identity uid=%d: %v", uid, err)
				}
			}
		}
		logger.Info("TELEGRAM linked uid=%d chat=%s", uid, chat)
		reply(lang, "tg.linked")
	case "/stop":
		if lookupErr == nil {
			if err := a.Store.SetTelegramChat(existing.ID, ""); err != nil {
				logger.Error("TELEGRAM stop uid=%d: %v", existing.ID, err)
				reply(lang, "tg.error")
				return
			}
			reply(lang, "tg.stopped")
		} else {
			reply(lang, "tg.status_unlinked")
		}
	default:
		reply(lang, "tg.help")
	}
}

// SetupTelegram registers the webhook with Telegram when the bot is configured.
func (a *App) SetupTelegram() {
	if !a.Bot.Configured() {
		logger.Info("TELEGRAM bot not configured: Telegram login and notifications are disabled")
		return
	}
	if !strings.HasPrefix(a.Cfg.BaseURL, "https://") {
		logger.Warn("TELEGRAM webhook not registered: base URL %q is not https", a.Cfg.BaseURL)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.Bot.SetWebhook(ctx, a.Cfg.BaseURL+"/api/telegram/webhook"); err != nil {
		logger.Error("TELEGRAM setWebhook failed: %v", err)
		return
	}
	logger.Info("TELEGRAM webhook registered at %s/api/telegram/webhook", a.Cfg.BaseURL)
}
