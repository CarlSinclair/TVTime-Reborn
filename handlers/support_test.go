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
	if !strings.HasPrefix(link, "https://t.me/tvt_test_bot?start=") {
		t.Fatalf("the link opens the bot with the token: %q", link)
	}
	token := strings.TrimPrefix(link, "https://t.me/tvt_test_bot?start=")
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
