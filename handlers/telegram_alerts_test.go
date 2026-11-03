package handlers_test

import (
	"strings"
	"testing"
)

func (e *env) setTelegramMaster(c *client, on bool) (int, map[string]any) {
	return c.post("/api/notifications/settings", map[string]any{
		"master": map[string]bool{"in_app": true, "push": false, "email": false, "telegram": on},
		"prefs":  map[string]any{},
	})
}

func TestTurningTelegramOnConfirmsOncePerFiveMinutes(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	a := e.mustUser(t, "alice")
	if err := e.store.SetTelegramChat(a.ID, "800"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetNotifyFlags(a.ID, true, false, false, false); err != nil {
		t.Fatal(err)
	}
	if code, out := e.setTelegramMaster(alice, true); code != 200 || out["ok"] != true {
		t.Fatalf("turn on: %d %v", code, out)
	}
	got := e.sentTo("800")
	if len(got) != 1 || !strings.HasPrefix(got[0], "✅ ") || !strings.Contains(got[0], "Notification Settings") || !strings.Contains(got[0], "/stop") {
		t.Fatalf("confirmation explains where to change it and how to stop: %v", got)
	}
	if u, _ := e.store.UserByID(a.ID); !u.NotifyTelegram {
		t.Fatalf("the switch is saved on")
	}
	e.setTelegramMaster(alice, false)
	e.setTelegramMaster(alice, true)
	if got := e.sentTo("800"); len(got) != 1 {
		t.Fatalf("switching on again within five minutes sends nothing more: %v", got)
	}
}

func TestSavingOtherPreferencesDoesNotRepeatTheConfirmation(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	a := e.mustUser(t, "alice")
	if err := e.store.SetTelegramChat(a.ID, "800"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetNotifyFlags(a.ID, true, false, false, true); err != nil {
		t.Fatal(err)
	}
	e.setTelegramMaster(alice, true)
	if got := e.sentTo("800"); len(got) != 0 {
		t.Fatalf("Telegram was already on, nothing to confirm: %v", got)
	}
}

func TestConfirmationTelegramRefusesKeepsTheSwitchOff(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	a := e.mustUser(t, "alice")
	if err := e.store.SetTelegramChat(a.ID, "666"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetNotifyFlags(a.ID, true, false, false, false); err != nil {
		t.Fatal(err)
	}
	code, out := e.setTelegramMaster(alice, true)
	if code != 400 || out["ok"] == true {
		t.Fatalf("a chat that blocked the bot is an error: %d %v", code, out)
	}
	if u, _ := e.store.UserByID(a.ID); u.NotifyTelegram || u.TelegramChatID != "" {
		t.Fatalf("the switch stays off and the dead chat is dropped: %+v", u)
	}
}

func TestStopTurnsAlertsOffAndKeepsTheLink(t *testing.T) {
	e := newEnv(t)
	alice := e.signup(t, "alice", true)
	a := e.mustUser(t, "alice")
	_, out := alice.post("/api/account/telegram/link-token", nil)
	token := strings.TrimPrefix(out["url"].(string), "tg://resolve?domain=tvt_test_bot&start=")
	e.tgUpdate(t, 800, "/start "+token, 0)
	e.tgUpdate(t, 800, "/stop", 0)
	got := e.sentTo("800")
	if len(got) != 2 || got[1] != "✅ Telegram alerts have been turned off. Turn them back on at any time in Notification Settings." {
		t.Fatalf("stop answers with the agreed text: %v", got)
	}
	if u, _ := e.store.UserByID(a.ID); u.NotifyTelegram || u.TelegramChatID != "800" {
		t.Fatalf("alerts are off but the chat stays linked: %+v", u)
	}
	e.tgUpdate(t, 900, "/stop", 0)
	if got := e.sentTo("900"); len(got) != 1 || !strings.HasPrefix(got[0], "❌ ") {
		t.Fatalf("an unlinked chat is told so: %v", got)
	}
}
