package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"tvt/logger"
	"tvt/store"
	"tvt/telegram"
)

// faqFor is the FAQ the bot shows someone: the entries in their language, or
// in the site's default language when theirs has none, or English.
func (a *App) faqFor(lang string) []store.FAQ {
	for _, l := range []string{lang, a.Cfg.DefaultLang, "en"} {
		list, err := a.Store.FAQList(l, true)
		if err != nil {
			logger.Error("TELEGRAM support faq lang=%s: %v", l, err)
			return nil
		}
		if len(list) > 0 {
			return list
		}
	}
	return nil
}

// helpButtons are the buttons under a bot reply that is not part of a
// conversation with a person: the questions (when there are any) and, always,
// the way to a human.
func (a *App) helpButtons(lang string) []telegram.Button {
	var out []telegram.Button
	if len(a.faqFor(lang)) > 0 {
		out = append(out, telegram.Button{Text: "❓ " + a.tgT(lang, "tg.faq_btn", nil), Data: "faq"})
	}
	return append(out, telegram.Button{Text: "💬 " + a.tgT(lang, "tg.human_btn", nil), Data: "human"})
}

func (a *App) tgSendButtons(chat, text string, buttons []telegram.Button) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := a.Bot.SendMessageButtons(ctx, chat, text, buttons); err != nil {
		logger.Error("TELEGRAM support send chat=%s: %v", chat, err)
	}
}

// tgSayHelp sends a reply with the help buttons under it.
func (a *App) tgSayHelp(chat, lang, text string) {
	a.tgSendButtons(chat, text, a.helpButtons(lang))
}

func (a *App) faqMenu(chat, lang string) {
	list := a.faqFor(lang)
	if len(list) == 0 {
		a.tgSayHelp(chat, lang, a.tgT(lang, "tg.help", nil))
		return
	}
	buttons := make([]telegram.Button, 0, len(list)+1)
	for _, f := range list {
		buttons = append(buttons, telegram.Button{Text: cleanChatText(f.Question, 60), Data: "faq:" + strconv.FormatInt(f.ID, 10)})
	}
	buttons = append(buttons, telegram.Button{Text: "💬 " + a.tgT(lang, "tg.human_btn", nil), Data: "human"})
	a.tgSendButtons(chat, a.tgT(lang, "tg.faq_intro", nil), buttons)
}

func (a *App) faqAnswer(chat, lang string, id int64) {
	f, err := a.Store.FAQGet(id)
	if err != nil || !f.Enabled {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			logger.Error("TELEGRAM support faq id=%d: %v", id, err)
		}
		a.tgSayHelp(chat, lang, "❌ "+a.tgT(lang, "tg.faq_gone", nil))
		return
	}
	a.tgSendButtons(chat, f.Question+"\n\n"+f.Answer, []telegram.Button{
		{Text: "« " + a.tgT(lang, "tg.faq_back", nil), Data: "faq"},
		{Text: "💬 " + a.tgT(lang, "tg.human_btn", nil), Data: "human"},
	})
}

// handleTelegramCallback runs a press of one of the buttons above.
func (a *App) handleTelegramCallback(cq *telegram.CallbackQuery) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.Bot.AnswerCallback(ctx, cq.ID); err != nil {
		logger.Error("TELEGRAM callback ack: %v", err)
	}
	if cq.Message == nil || cq.Message.Chat.Type != "private" {
		return
	}
	chat := strconv.FormatInt(cq.Message.Chat.ID, 10)
	lang := tgLangFor(a, cq.From.LanguageCode)
	var linked *store.User
	if u, err := a.Store.UserByTelegramChat(chat); err == nil {
		lang, linked = u.Locale, u
	} else if !errors.Is(err, store.ErrNotFound) {
		logger.Error("TELEGRAM callback lookup chat=%s: %v", chat, err)
	}
	switch {
	case cq.Data == "human":
		a.supportBegin(&telegram.Message{From: &cq.From, Chat: cq.Message.Chat}, chat, lang, linked)
	case cq.Data == "faq":
		a.faqMenu(chat, lang)
	case strings.HasPrefix(cq.Data, "faq:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(cq.Data, "faq:"), 10, 64)
		if err != nil {
			return
		}
		a.faqAnswer(chat, lang, id)
	}
}

// apiAdminFAQ adds, saves and deletes the support bot's questions.
func (a *App) apiAdminFAQ(w http.ResponseWriter, r *http.Request) {
	if _, err := a.needAdmin(r); err != nil {
		a.fail(w, r, err)
		return
	}
	var in map[string]any
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	pos, _ := strconv.Atoi(strings.TrimSpace(strOf(in, "position")))
	f := store.FAQ{
		Lang: strOf(in, "lang"), Position: pos, Enabled: boolOf(in, "enabled"),
		Question: strings.TrimSpace(strOf(in, "question")), Answer: strings.TrimSpace(strOf(in, "answer")),
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(strOf(in, "id")), 10, 64)
	switch strOf(in, "action") {
	case "add":
		if !a.I18n.Has(f.Lang) || f.Question == "" || f.Answer == "" || utf8.RuneCountInString(f.Question) > 200 || utf8.RuneCountInString(f.Answer) > 3500 {
			a.fail(w, r, errBadRequest)
			return
		}
		if err := a.Store.FAQAdd(f); err != nil {
			a.fail(w, r, err)
			return
		}
	case "save":
		if id == 0 {
			a.fail(w, r, errBadRequest)
			return
		}
		if boolOf(in, "delete") {
			if err := a.Store.FAQDelete(id); err != nil {
				a.fail(w, r, err)
				return
			}
			break
		}
		if f.Question == "" || f.Answer == "" || utf8.RuneCountInString(f.Question) > 200 || utf8.RuneCountInString(f.Answer) > 3500 {
			a.fail(w, r, errBadRequest)
			return
		}
		f.ID = id
		if err := a.Store.FAQSave(f); err != nil {
			a.fail(w, r, err)
			return
		}
	default:
		a.fail(w, r, errBadRequest)
		return
	}
	a.okJSON(w, nil)
}
