package handlers_test

import (
	"strings"
	"testing"

	"tvt/appconfig"
)

func TestEnvironmentSettingsAreImportedOnceAndThenIgnored(t *testing.T) {
	e := newEnv(t)
	env := *e.cfg
	env.SMTPHost, env.SMTPPort, env.TMDBKey, env.AppName = "mail.env.test", 587, "envkey-123", "TVT"
	m := appconfig.New(e.store, env.JWTSecret, &env)
	keys, err := m.ImportEnv()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(keys, ",")
	if !strings.Contains(got, "smtp_host") || !strings.Contains(got, "tmdb_key") || strings.Contains(got, "smtp_port") || strings.Contains(got, "app_name") {
		t.Fatalf("only values that differ from the defaults are imported: %s", got)
	}
	if raw := e.store.KVGet("cfg.tmdb_key"); raw == "" || strings.Contains(raw, "envkey-123") {
		t.Fatalf("an imported secret is stored sealed: %q", raw)
	}
	if again, _ := m.ImportEnv(); len(again) != 0 {
		t.Fatalf("the import runs once: %v", again)
	}

	changed := env
	changed.SMTPHost, changed.TMDBKey = "other.env.test", "otherkey"
	m2 := appconfig.New(e.store, env.JWTSecret, &changed)
	c, err := m2.Overlay(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.SMTPHost != "mail.env.test" || c.TMDBKey != "envkey-123" {
		t.Fatalf("after the import the database wins and the environment is not read: %s %s", c.SMTPHost, c.TMDBKey)
	}
	vals, _ := m2.Values("mail")
	for _, v := range vals {
		if v.Key == "smtp_host" && v.Text != "mail.env.test" {
			t.Fatalf("the field shows the current value: %+v", v)
		}
	}
}
