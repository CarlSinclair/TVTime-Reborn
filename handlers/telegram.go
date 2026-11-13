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
	"tvt/i18n"

	"tvt/auth"
	"tvt/logger"
	"tvt/store"
	"tvt/telegram"

	"github.com/CarlSinclair/csuit-libs/telegramlogin"
)

func hashLinkToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func tgLangFor(a *App, code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return i18n.Fallback
	}
	return a.I18n.Pick(code)
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

// confirmTelegramOn sends the "notifications are on" message when the user
// switches the Telegram channel on, at most once per five minutes. A message
// Telegram refuses means the toggle would not work, so the error is returned.
func (a *App) confirmTelegramOn(ctx context.Context, u *store.User) error {
	if !a.Bot.Configured() || u.TelegramChatID == "" {
		return nil
	}
	if !a.tgOnLimit.Allow(itoa(u.ID)) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := a.Bot.SendMessage(ctx, u.TelegramChatID, "✅ "+a.tgT(u.Locale, "tg.dm_enabled", nil))
	if err == nil {
		return nil
	}
	if errors.Is(err, telegram.ErrBotBlocked) {
		logger.Warn("TELEGRAM confirm on uid=%d chat=%s: bot blocked, chat dropped: %v", u.ID, u.TelegramChatID, err)
		if cerr := a.Store.SetTelegramChat(u.ID, ""); cerr != nil {
			logger.Error("TELEGRAM clear dead chat uid=%d: %v", u.ID, cerr)
		}
		return apiErr{http.StatusBadRequest, "err.telegram_start_bot"}
	}
	logger.Error("TELEGRAM confirm on uid=%d chat=%s: %v", u.ID, u.TelegramChatID, err)
	return apiErr{http.StatusBadGateway, "err.telegram_send_failed"}
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
	if upd.CallbackQuery != nil {
		a.handleTelegramCallback(upd.CallbackQuery)
		return
	}
	msg := upd.Message
	if msg == nil {
		return
	}
	cmd, arg := telegram.ParseCommand(msg.Text)
	chat := strconv.FormatInt(msg.Chat.ID, 10)
	hint := ""
	if msg.From != nil {
		hint = msg.From.LanguageCode
	}
	reply := func(lang, key string) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		text := a.tgT(lang, "tg."+key, nil)
		switch key {
		case "linked", "status_linked", "stopped":
			text = "✅ " + text
		case "link_invalid", "error", "private_only", "status_unlinked":
			text = "❌ " + text
		}
		var buttons []telegram.Button
		if msg.Chat.Type == "private" {
			buttons = a.helpButtons(lang)
		}
		if _, err := a.Bot.SendMessageButtons(ctx, chat, text, buttons); err != nil {
			logger.Error("TELEGRAM reply chat=%s key=%s: %v", chat, key, err)
		}
	}
	lang := tgLangFor(a, hint)
	if msg.Chat.Type != "private" {
		if g := a.supportGroup(); g != "" && g == chat {
			if msg.ReplyToMessage != nil && msg.From != nil && !msg.From.IsBot {
				a.supportOperatorReply(msg, chat, lang)
			}
			return
		}
		reply(lang, "private_only")
		return
	}
	existing, lookupErr := a.Store.UserByTelegramChat(chat)
	if lookupErr == nil {
		lang = existing.Locale
	} else if !errors.Is(lookupErr, store.ErrNotFound) {
		logger.Error("TELEGRAM lookup chat=%s: %v", chat, lookupErr)
		reply(lang, "error")
		return
	}
	if lookupErr == nil && existing.IsAdmin && msg.ReplyToMessage != nil {
		a.supportOperatorReply(msg, chat, lang)
		return
	}
	var linked *store.User
	if lookupErr == nil {
		linked = existing
	}
	if cmd == "" {
		if !a.supportText(msg, chat, lang) {
			reply(lang, "help")
		}
		return
	}
	if cmd == "/support" || (cmd == "/start" && strings.EqualFold(arg, "support")) {
		a.supportBegin(msg, chat, lang, linked)
		return
	}
	switch cmd {
	case "/faq", "/help":
		a.faqMenu(chat, lang)
	case "/close":
		a.supportClose(chat, lang)
	case "/start":
		if telegramlogin.IsTelegramLoginToken(arg) {
			a.telegramLoginStart(msg, chat, lang, arg)
			return
		}
		if arg == "" {
			if lookupErr == nil {
				reply(lang, "status_linked")
			} else {
				reply(lang, "status_unlinked")
			}
			return
		}
		uid, err := a.Store.ConsumeTelegramLinkToken(hashLinkToken(arg))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				reply(lang, "link_invalid")
			} else {
				logger.Error("TELEGRAM consume link token chat=%s: %v", chat, err)
				reply(lang, "error")
			}
			return
		}
		if err := a.Store.SetTelegramChatClearOthers(uid, chat); err != nil {
			logger.Error("TELEGRAM link uid=%d chat=%s: %v", uid, chat, err)
			reply(lang, "error")
			return
		}
		if u, err := a.Store.UserByID(uid); err == nil {
			lang = u.Locale
			if err := a.Store.SetNotifyFlags(uid, u.NotifyInApp, u.NotifyPush, u.NotifyEmail, true); err != nil {
				logger.Error("TELEGRAM could not turn on Telegram notifications uid=%d: %v", uid, err)
			}
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
		reply(lang, "linked")
	case "/stop":
		if lookupErr == nil {
			if err := a.Store.SetNotifyFlags(existing.ID, existing.NotifyInApp, existing.NotifyPush, existing.NotifyEmail, false); err != nil {
				logger.Error("TELEGRAM stop uid=%d: %v", existing.ID, err)
				reply(lang, "error")
				return
			}
			reply(lang, "stopped")
		} else {
			reply(lang, "status_unlinked")
		}
	default:
		reply(lang, "help")
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
