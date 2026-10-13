package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"tvt/logger"
	"tvt/store"
	"tvt/telegram"
)

const supportKeep = 90 * 24 * time.Hour

func (a *App) tgSend(chat, text string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return a.Bot.SendMessageID(ctx, chat, text)
}

func (a *App) tgSay(chat, text string) {
	if _, err := a.tgSend(chat, text); err != nil {
		logger.Error("TELEGRAM support send chat=%s: %v", chat, err)
	}
}

func (a *App) tgT(lang, key string, vars map[string]string) string {
	if vars == nil {
		vars = map[string]string{}
	}
	vars["app"] = a.Cfg.AppName
	return a.I18n.TN(lang, key, vars)
}

func cleanChatText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// supportBegin starts a support conversation: the bot greets the person and
// asks for their name.
func (a *App) supportBegin(msg *telegram.Message, chat, lang string, linked *store.User) {
	ops, err := a.Store.OperatorChats()
	if err != nil {
		logger.Error("TELEGRAM support operators: %v", err)
		a.tgSay(chat, "❌ "+a.I18n.T(lang, "tg.error"))
		return
	}
	if len(ops) == 0 {
		a.tgSay(chat, "❌ "+a.tgT(lang, "tg.support_unavailable", nil))
		return
	}
	if err := a.allow(a.supportLimit, "sup:"+chat); err != nil {
		a.tgSay(chat, "❌ "+a.I18n.T(lang, "tg.error"))
		return
	}
	if err := a.Store.PurgeSupport(store.Ts(time.Now().Add(-supportKeep))); err != nil {
		logger.Error("TELEGRAM support purge: %v", err)
	}
	var uid int64
	if linked != nil {
		uid = linked.ID
	}
	if _, err := a.Store.StartSupportThread(chat, lang, uid); err != nil {
		logger.Error("TELEGRAM support start chat=%s: %v", chat, err)
		a.tgSay(chat, "❌ "+a.I18n.T(lang, "tg.error"))
		return
	}
	a.tgSay(chat, a.tgT(lang, "tg.support_hello", nil))
}

// supportHeader describes who a relayed message is from.
func (a *App) supportHeader(t *store.SupportThread, msg *telegram.Message, first bool) string {
	who := t.Name
	if who == "" {
		who = "?"
	}
	if !first {
		return fmt.Sprintf("#%d %s:", t.ID, who)
	}
	extra := []string{}
	if msg.From != nil && msg.From.Username != "" {
		extra = append(extra, "@"+msg.From.Username)
	}
	if t.UserID != 0 {
		if u, err := a.Store.UserByID(t.UserID); err == nil {
			extra = append(extra, "account @"+u.Username)
		}
	}
	extra = append(extra, t.Lang)
	return fmt.Sprintf("🆘 Support #%d from %s (%s):", t.ID, who, strings.Join(extra, ", "))
}

// supportForward relays a visitor's message to every operator chat and
// remembers each relayed message so the operator can answer it by replying.
func (a *App) supportForward(t *store.SupportThread, msg *telegram.Message, first bool) bool {
	ops, err := a.Store.OperatorChats()
	if err != nil || len(ops) == 0 {
		if err != nil {
			logger.Error("TELEGRAM support operators: %v", err)
		}
		return false
	}
	text := a.supportHeader(t, msg, first) + "\n\n" + msg.Text
	delivered := false
	for _, op := range ops {
		id, err := a.tgSend(op, text)
		if err != nil {
			logger.Error("TELEGRAM support relay thread=%d operator=%s: %v", t.ID, op, err)
			continue
		}
		delivered = true
		if id != 0 {
			if err := a.Store.AddSupportRelay(op, id, t.ID); err != nil {
				logger.Error("TELEGRAM support relay record thread=%d: %v", t.ID, err)
			}
		}
	}
	return delivered
}

// supportText handles a plain message from someone in a support conversation.
// It returns false when the chat has no conversation, so the caller can answer
// with the usual help text.
func (a *App) supportText(msg *telegram.Message, chat, lang string) bool {
	t, err := a.Store.ActiveSupportThread(chat)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			logger.Error("TELEGRAM support thread chat=%s: %v", chat, err)
		}
		return false
	}
	if t.Lang != "" {
		lang = t.Lang
	}
	if err := a.allow(a.supportLimit, "sup:"+chat); err != nil {
		a.tgSay(chat, "❌ "+a.I18n.T(lang, "tg.error"))
		return true
	}
	switch t.State {
	case "name":
		name := cleanChatText(msg.Text, 60)
		if name == "" {
			a.tgSay(chat, a.tgT(lang, "tg.support_hello", nil))
			return true
		}
		if err := a.Store.SetSupportName(t.ID, name); err != nil {
			logger.Error("TELEGRAM support name: %v", err)
			a.tgSay(chat, "❌ "+a.I18n.T(lang, "tg.error"))
			return true
		}
		a.tgSay(chat, a.tgT(lang, "tg.support_ask_issue", map[string]string{"name": name}))
	case "issue":
		if !a.supportForward(t, msg, true) {
			a.tgSay(chat, "❌ "+a.tgT(lang, "tg.support_unavailable", nil))
			return true
		}
		if err := a.Store.SetSupportState(t.ID, "open"); err != nil {
			logger.Error("TELEGRAM support open: %v", err)
		}
		a.tgSay(chat, "✅ "+a.tgT(lang, "tg.support_received", map[string]string{"name": t.Name}))
	default:
		if !a.supportForward(t, msg, false) {
			a.tgSay(chat, "❌ "+a.tgT(lang, "tg.support_unavailable", nil))
		}
	}
	return true
}

// supportClose ends a conversation from the visitor's side.
func (a *App) supportClose(chat, lang string) {
	t, err := a.Store.ActiveSupportThread(chat)
	if err != nil {
		a.tgSay(chat, a.tgT(lang, "tg.support_none", nil))
		return
	}
	if err := a.Store.SetSupportState(t.ID, "closed"); err != nil {
		logger.Error("TELEGRAM support close: %v", err)
		a.tgSay(chat, "❌ "+a.I18n.T(lang, "tg.error"))
		return
	}
	a.tgSay(chat, "✅ "+a.tgT(lang, "tg.support_closed", nil))
	if ops, err := a.Store.OperatorChats(); err == nil {
		for _, op := range ops {
			a.tgSay(op, fmt.Sprintf("#%d %s closed the conversation.", t.ID, t.Name))
		}
	}
}

// supportOperatorReply sends an operator's reply to a relayed message back to
// the person it came from. Replying "/close" ends the conversation.
func (a *App) supportOperatorReply(msg *telegram.Message, opChat, opLang string) {
	t, err := a.Store.SupportThreadByRelay(opChat, msg.ReplyToMessage.MessageID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.tgSay(opChat, "❌ "+a.tgT(opLang, "tg.support_op_unknown", nil))
		} else {
			logger.Error("TELEGRAM support relay lookup: %v", err)
			a.tgSay(opChat, "❌ "+a.I18n.T(opLang, "tg.error"))
		}
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "/close" {
		if err := a.Store.SetSupportState(t.ID, "closed"); err != nil {
			logger.Error("TELEGRAM support close: %v", err)
			a.tgSay(opChat, "❌ "+a.I18n.T(opLang, "tg.error"))
			return
		}
		a.tgSay(t.ChatID, "✅ "+a.tgT(t.Lang, "tg.support_closed", nil))
		a.tgSay(opChat, "✅ "+a.tgT(opLang, "tg.support_op_closed", map[string]string{"id": fmt.Sprint(t.ID)}))
		return
	}
	if t.State == "closed" {
		if err := a.Store.SetSupportState(t.ID, "open"); err != nil {
			logger.Error("TELEGRAM support reopen: %v", err)
		}
	}
	if _, err := a.tgSend(t.ChatID, text); err != nil {
		logger.Error("TELEGRAM support deliver thread=%d: %v", t.ID, err)
		a.tgSay(opChat, "❌ "+a.tgT(opLang, "tg.support_op_failed", map[string]string{"id": fmt.Sprint(t.ID)}))
	}
}
