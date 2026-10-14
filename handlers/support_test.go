package handlers_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func (e *env) tgUpdate(t *testing.T, chat int64, text string, replyTo int64) {
	t.Helper()
	reply := ""
	if replyTo != 0 {
		reply = fmt.Sprintf(`,"reply_to_message":{"message_id":%d,"chat":{"id":%d,"type":"private"}}`, replyTo, chat)
	}
	body := fmt.Sprintf(`{"update_id":1,"message":{"message_id":5,"text":%q,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"username":"visitor","first_name":"V","language_code":"en"}%s}}`, text, chat, chat, reply)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/telegram/webhook", strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "whsecret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func (e *env) sentTo(chat string) []string {
	e.tg.mu.Lock()
	defer e.tg.mu.Unlock()
	var out []string
	for _, m := range e.tg.sent {
		if c, _ := m["chat_id"].(string); c == chat {
			s, _ := m["text"].(string)
			out = append(out, s)
		}
	}
	return out
}

func (e *env) lastMessageID(chat string) int64 {
	e.tg.mu.Lock()
	defer e.tg.mu.Unlock()
	for i := len(e.tg.sent) - 1; i >= 0; i-- {
		if c, _ := e.tg.sent[i]["chat_id"].(string); c == chat {
			return int64(1000 + i + 1)
		}
	}
	return 0
}

func TestTelegramSupportRelaysBothWays(t *testing.T) {
	e := newEnv(t)
	e.makeAdmin(t, "boss")
	if err := e.store.SetTelegramChat(e.mustUser(t, "boss").ID, "900"); err != nil {
		t.Fatal(err)
	}

	e.tgUpdate(t, 700, "/start support", 0)
	if got := e.sentTo("700"); len(got) != 1 || !strings.Contains(got[0], "support bot") || !strings.Contains(got[0], "your name") {
		t.Fatalf("the bot greets and asks for a name: %v", got)
	}
	e.tgUpdate(t, 700, "Pat", 0)
	if got := e.sentTo("700"); len(got) != 2 || !strings.Contains(got[1], "Nice to meet you, Pat") {
		t.Fatalf("it greets them again by name and asks what is wrong: %v", got)
	}
	e.tgUpdate(t, 700, "My episodes will not sync", 0)
	ops := e.sentTo("900")
	if len(ops) != 1 || !strings.Contains(ops[0], "🆘 Support #1 from Pat") || !strings.Contains(ops[0], "My episodes will not sync") || !strings.Contains(ops[0], "@visitor") {
		t.Fatalf("the request is passed to the operator: %v", ops)
	}
	if got := e.sentTo("700"); !strings.HasPrefix(got[len(got)-1], "✅ ") {
		t.Fatalf("the visitor is told it was sent, with a green check: %v", got)
	}

	e.tgUpdate(t, 900, "Try signing out and in", e.lastMessageID("900"))
	if got := e.sentTo("700"); got[len(got)-1] != "Try signing out and in" {
		t.Fatalf("the operator's reply goes to the visitor through the bot: %v", got)
	}
	e.tgUpdate(t, 700, "That worked, thanks", 0)
	ops = e.sentTo("900")
	if !strings.Contains(ops[len(ops)-1], "#1 Pat:") || !strings.Contains(ops[len(ops)-1], "That worked, thanks") {
		t.Fatalf("the visitor's reply comes back to the operator: %v", ops)
	}
	e.tgUpdate(t, 900, "/close", e.lastMessageID("900"))
	if got := e.sentTo("700"); !strings.Contains(got[len(got)-1], "closed") {
		t.Fatalf("the operator can close it: %v", got)
	}
	n := len(e.sentTo("900"))
	e.tgUpdate(t, 700, "hello?", 0)
	if len(e.sentTo("900")) != n {
		t.Fatalf("a closed conversation relays nothing")
	}
	e.tgUpdate(t, 900, "random text that is not a reply", 0)
	e.tgUpdate(t, 900, "orphan", 424242)
	if got := e.sentTo("900"); !strings.HasPrefix(got[len(got)-1], "❌ ") {
		t.Fatalf("an unmatched reply is refused with a red cross: %v", got)
	}
}

func TestTelegramSupportNeedsAnOperator(t *testing.T) {
	e := newEnv(t)
	e.tgUpdate(t, 701, "/support", 0)
	if got := e.sentTo("701"); len(got) != 1 || !strings.HasPrefix(got[0], "❌ ") {
		t.Fatalf("with nobody to answer, the bot says so: %v", got)
	}
}

func TestConnectingTelegramTurnsOnNotificationsAndAnswersInTheUsersLanguage(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	a := e.mustUser(t, "alice")
	if _, err := e.store.DB.Exec(`UPDATE users SET locale = 'pt', notify_telegram = 0 WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	_, out := alice.post("/api/account/telegram/link-token", nil)
	link, _ := out["url"].(string)
	if !strings.HasPrefix(link, "tg://resolve?domain=tvt_test_bot&start=") {
		t.Fatalf("the link opens the bot with the token: %q", link)
	}
	token := strings.TrimPrefix(link, "tg://resolve?domain=tvt_test_bot&start=")
	e.tgUpdate(t, 800, "/start "+token, 0)
	got := e.sentTo("800")
	if len(got) != 1 || !strings.HasPrefix(got[0], "✅ Conectado!") || !strings.Contains(got[0], "TVT") || !strings.Contains(got[0], "Boa diversão!") {
		t.Fatalf("success is a green check and the text in the account's language: %v", got)
	}
	if u, _ := e.store.UserByID(a.ID); !u.NotifyTelegram || u.TelegramChatID != "800" {
		t.Fatalf("Telegram notifications are turned on: %+v", u)
	}
	e.tgUpdate(t, 801, "/start not-a-real-token", 0)
	if got := e.sentTo("801"); len(got) != 1 || !strings.HasPrefix(got[0], "❌ ") {
		t.Fatalf("failure is a red cross: %v", got)
	}
}

func (e *env) tgGroupUpdate(t *testing.T, chat int64, text string, replyTo int64) {
	t.Helper()
	body := fmt.Sprintf(`{"update_id":1,"message":{"message_id":6,"text":%q,"chat":{"id":%d,"type":"group"},"from":{"id":55,"username":"agent","first_name":"A","language_code":"en"},"reply_to_message":{"message_id":%d,"chat":{"id":%d,"type":"group"}}}}`, text, chat, replyTo, chat)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/telegram/webhook", strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "whsecret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestTelegramSupportCanGoToAGroupChat(t *testing.T) {
	e := newEnv(t)
	e.makeAdmin(t, "boss")
	if err := e.store.SetTelegramChat(e.mustUser(t, "boss").ID, "900"); err != nil {
		t.Fatal(err)
	}
	e.cfg.SupportToGroup, e.cfg.SupportChatID = true, "-500"

	e.tgUpdate(t, 700, "/support", 0)
	e.tgUpdate(t, 700, "Pat", 0)
	e.tgUpdate(t, 700, "Cannot log in", 0)
	if got := e.sentTo("-500"); len(got) != 1 || !strings.Contains(got[0], "Cannot log in") {
		t.Fatalf("the request goes to the group: %v", got)
	}
	if got := e.sentTo("900"); len(got) != 0 {
		t.Fatalf("the admin is not messaged directly: %v", got)
	}
	e.tgGroupUpdate(t, -500, "Try resetting your password", e.lastMessageID("-500"))
	if got := e.sentTo("700"); got[len(got)-1] != "Try resetting your password" {
		t.Fatalf("a reply in the group reaches the visitor: %v", got)
	}
	n := len(e.sentTo("-500"))
	e.tgGroupUpdate(t, -500, "unrelated", 424242)
	if len(e.sentTo("-500")) != n {
		t.Fatalf("the group is not spammed by replies that are not support")
	}
	e.cfg.SupportToGroup = false
	e.tgGroupUpdate(t, -500, "ignored", e.lastMessageID("-500"))
	if got := e.sentTo("700"); got[len(got)-1] == "ignored" {
		t.Fatalf("with the switch off the group is not an operator")
	}
}

func (e *env) tgCallback(t *testing.T, chat int64, data string) {
	t.Helper()
	body := fmt.Sprintf(`{"update_id":2,"callback_query":{"id":"cb1","data":%q,"from":{"id":%d,"username":"visitor","first_name":"V","language_code":"en"},"message":{"message_id":7,"chat":{"id":%d,"type":"private"}}}}`, data, chat, chat)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/telegram/webhook", strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "whsecret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func (e *env) lastTo(chat string) map[string]any {
	msgs := e.tg.messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if c, _ := msgs[i]["chat_id"].(string); c == chat {
			return msgs[i]
		}
	}
	return nil
}

func buttonsOf(m map[string]any) []string {
	var out []string
	markup, _ := m["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	for _, r := range rows {
		for _, b := range r.([]any) {
			out = append(out, b.(map[string]any)["callback_data"].(string))
		}
	}
	return out
}

func TestSupportBotOffersTheFAQAndAlwaysAWayToAHuman(t *testing.T) {
	e := newEnv(t)
	boss := e.makeAdmin(t, "boss")
	if err := e.store.SetTelegramChat(e.mustUser(t, "boss").ID, "900"); err != nil {
		t.Fatal(err)
	}

	e.tgUpdate(t, 700, "hello?", 0)
	if got := buttonsOf(e.lastTo("700")); len(got) != 1 || got[0] != "human" {
		t.Fatalf("with no questions the reply still carries the human button: %v", got)
	}

	if code, _ := boss.post("/api/admin/faq", map[string]any{"action": "add", "lang": "en", "question": "How do I sync?", "answer": "Press the sync button.", "position": "1", "enabled": true}); code != 200 {
		t.Fatalf("add a question: %d", code)
	}
	e.tgUpdate(t, 700, "hello?", 0)
	if got := buttonsOf(e.lastTo("700")); len(got) != 2 || got[0] != "faq" || got[1] != "human" {
		t.Fatalf("with questions the reply offers them and the human button: %v", got)
	}

	e.tgUpdate(t, 700, "/faq", 0)
	if got := buttonsOf(e.lastTo("700")); len(got) != 2 || got[0] != "faq:1" || got[1] != "human" {
		t.Fatalf("the question menu lists each question and the human button: %v", got)
	}
	e.tgCallback(t, 700, "faq:1")
	last := e.lastTo("700")
	if txt, _ := last["text"].(string); !strings.Contains(txt, "Press the sync button.") {
		t.Fatalf("pressing a question answers it: %v", last)
	}
	if got := buttonsOf(last); len(got) != 2 || got[0] != "faq" || got[1] != "human" {
		t.Fatalf("an answer offers the way back and the human button: %v", got)
	}

	e.tgCallback(t, 700, "human")
	if txt, _ := e.lastTo("700")["text"].(string); !strings.Contains(txt, "your name") {
		t.Fatalf("the human button starts a conversation: %q", txt)
	}
	e.tgUpdate(t, 700, "Pat", 0)
	e.tgUpdate(t, 700, "Need help", 0)
	e.tgUpdate(t, 700, "/close", 0)
	if got := buttonsOf(e.lastTo("700")); len(got) == 0 || got[len(got)-1] != "human" {
		t.Fatalf("closing a conversation leaves the human button: %v", got)
	}

	if code, _ := boss.post("/api/admin/faq", map[string]any{"action": "save", "id": "1", "delete": true}); code != 200 {
		t.Fatalf("delete a question: %d", code)
	}
	if list, _ := e.store.FAQList("", false); len(list) != 0 {
		t.Fatalf("the question is gone: %+v", list)
	}
}
