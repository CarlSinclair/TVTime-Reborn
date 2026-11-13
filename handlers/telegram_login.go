package handlers

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tvt/auth"
	"tvt/logger"
	"tvt/store"
	"tvt/telegram"

	"github.com/CarlSinclair/csuit-libs/telegramlogin"
)

const tgCallback = "tgl:"

var tgSlugRE = regexp.MustCompile(`[^a-z0-9]+`)

func hostLabel(baseURL string) string {
	host := strings.ToLower(strings.TrimSpace(baseURL))
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	if i := strings.Index(host, "."); i >= 0 {
		host = host[:i]
	}
	return host
}

// instanceName is the sign-in token's tenant name: the site name set in Admin
// Config reduced to a-z, 0-9 and hyphens, or the first host label when the
// site name has nothing usable in it.
func instanceName(appName, baseURL string) string {
	label := strings.Trim(tgSlugRE.ReplaceAllString(strings.ToLower(appName), "-"), "-")
	if label == "" {
		label = strings.Trim(tgSlugRE.ReplaceAllString(hostLabel(baseURL), "-"), "-")
	}
	if len(label) < 3 {
		label = "tvt-" + label
	}
	if len(label) > 32 {
		label = strings.Trim(label[:32], "-")
	}
	return label
}

var tgLoginKeys = map[string]string{
	"tg_link_invalid":       "tg.login_invalid",
	"tg_sys_err":            "tg.error",
	"tg_login_ask":          "tg.login_ask",
	"tg_login_confirm_btn":  "tg.login_confirm_btn",
	"tg_login_done":         "tg.login_done",
	"tg_login_private_only": "tg.private_only",
	"tg_login_not_linked":   "err.signups_closed",
}

func (a *App) configureTelegramLogin() {
	telegramlogin.Configure(telegramlogin.Config{
		OwnDirName: func() string { return instanceName(a.Cfg.AppName, a.Cfg.BaseURL) },
		Resolve:    a.telegramResolve,
		Text: func(lang, key string) string {
			mapped, ok := tgLoginKeys[key]
			if !ok {
				logger.Error("TELEGRAM login: no text key mapped for %q", key)
				mapped = "tg.error"
			}
			return a.tgT(lang, mapped, nil)
		},
	})
}

func (a *App) telegramResolve(chatID int64, who telegramlogin.Identity) ([]int64, error) {
	if chatID <= 0 {
		return nil, nil
	}
	u, err := a.Store.UserByTelegramID(chatID)
	switch {
	case err == nil:
		if who.Username != u.TelegramUsername {
			if lerr := a.Store.LinkTelegram(u.ID, chatID, who.Username); lerr != nil {
				logger.Warn("AUTH telegram refresh username uid=%d tg=%d: %v", u.ID, chatID, lerr)
			}
		}
		return []int64{u.ID}, nil
	case errors.Is(err, store.ErrNotFound):
		if a.signupsClosed() {
			logger.Info("AUTH telegram sign-in refused: sign-ups are closed tg=%d", chatID)
			return nil, nil
		}
		lang := who.Lang
		if lang == "" {
			lang = "en"
		}
		u, err = a.Store.CreateTelegramUser(chatID, who.Username, strings.TrimSpace(who.FirstName), lang)
		if err != nil {
			logger.Error("AUTH telegram register tg=%d: %v", chatID, err)
			return nil, err
		}
		logger.Info("AUTH telegram registered uid=%d tg=%d", u.ID, chatID)
		return []int64{u.ID}, nil
	}
	logger.Error("AUTH telegram lookup tg=%d: %v", chatID, err)
	return nil, err
}

func (a *App) telegramLoginHTTP() telegramlogin.HTTPConfig {
	return telegramlogin.HTTPConfig{
		BotUsername: func() string { return a.Bot.Username },
		Where:       auth.ClientCountry,
		LookupUser: func(id int64) (telegramlogin.Subject, error) {
			u, err := a.Store.UserByID(id)
			if err != nil {
				return telegramlogin.Subject{}, err
			}
			return telegramlogin.Subject{ID: u.ID, Disabled: u.IsDeactivated && !u.SelfDeactivated, Payload: u}, nil
		},
		LogSignIn: func(r *http.Request, s telegramlogin.Subject) {
			logger.Info("AUTH telegram sign-in uid=%d", s.ID)
		},
		Temp2FAToken: func(userID int64) (string, error) {
			return "", errors.New("two-factor sign-in is not used for Telegram sign-in here")
		},
		Finalize: func(w http.ResponseWriter, r *http.Request, s telegramlogin.Subject) {
			u, ok := s.Payload.(*store.User)
			if !ok {
				logger.Error("AUTH telegram sign-in: unexpected subject payload uid=%d", s.ID)
				http.Redirect(w, r, "/login?error=internal", http.StatusSeeOther)
				return
			}
			a.finishLoginRedirect(w, r, u)
		},
	}
}

func (a *App) apiTelegramLoginStart(w http.ResponseWriter, r *http.Request) {
	if !a.Bot.Configured() {
		a.fail(w, r, apiErr{http.StatusServiceUnavailable, "err.telegram_unavailable"})
		return
	}
	if err := a.allow(a.authLimit, "tg:"+auth.ClientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	secret, err := telegramlogin.NewTelegramLoginSecret()
	if err != nil {
		logger.Error("AUTH telegram login start: secret generation failed: %v", err)
		a.fail(w, r, err)
		return
	}
	token, err := telegramlogin.CreateTelegramLoginToken(secret, auth.ClientCountry(r))
	if err != nil {
		logger.Error("AUTH telegram login start: token creation failed: %v", err)
		a.fail(w, r, apiErr{http.StatusServiceUnavailable, "err.telegram_unavailable"})
		return
	}
	a.okJSON(w, map[string]any{
		"token":  token,
		"secret": secret,
		"app":    a.Bot.DeepLink(token),
		"web":    "https://t.me/" + a.Bot.Username + "?start=" + token,
	})
}

func (a *App) pageTelegramLoginPoll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a.telegramLoginHTTP().HandlePoll(w, r)
}

func (a *App) telegramLoginStart(msg *telegram.Message, chat, lang, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if msg.From == nil || msg.From.ID == 0 {
		a.sendLoginReply(ctx, chat, "❌ "+a.tgT(lang, "tg.login_invalid", nil), "", "")
		return
	}
	_, reply, confirm, label := telegramlogin.ProcessTelegramLoginStartAs(token, msg.Chat.ID, msg.Chat.Type, lang, telegramlogin.Identity{
		Username: msg.From.Username, FirstName: msg.From.FirstName, Lang: lang,
	})
	a.sendLoginReply(ctx, chat, reply, confirm, label)
}

func (a *App) telegramLoginConfirm(cq *telegram.CallbackQuery, lang string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	token := strings.TrimPrefix(cq.Data, tgCallback)
	chat := strconv.FormatInt(cq.Message.Chat.ID, 10)
	_, reply := telegramlogin.ProcessTelegramLoginConfirm(token, cq.Message.Chat.ID, lang)
	if reply == "" {
		reply = "❌ " + a.tgT(lang, "tg.login_invalid", nil)
	}
	a.sendLoginReply(ctx, chat, reply, "", "")
}

func (a *App) sendLoginReply(ctx context.Context, chat, text, token, label string) {
	var buttons []telegram.Button
	if token != "" {
		buttons = []telegram.Button{{Text: label, Data: tgCallback + token}}
	}
	if _, err := a.Bot.SendMessageButtons(ctx, chat, text, buttons); err != nil {
		logger.Error("TELEGRAM login reply chat=%s: %v", chat, err)
	}
}
