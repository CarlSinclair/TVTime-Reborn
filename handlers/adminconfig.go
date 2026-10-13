package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"tvt/appconfig"
	"tvt/config"
	"tvt/logger"
	"tvt/mailer"
	"tvt/media"
	"tvt/telegram"
)

type cfgGroup struct {
	Name   string
	Values []appconfig.Value
}

func (a *App) configPageData(data map[string]any) error {
	var groups []cfgGroup
	for _, g := range appconfig.Groups {
		vals, err := a.Settings.Values(g)
		if err != nil {
			return err
		}
		groups = append(groups, cfgGroup{Name: g, Values: vals})
	}
	data["Groups"] = groups
	return a.pendingData(data)
}

// pendingData says whether saved settings differ from what is running, which
// is the case after a change until the server restarts.
func (a *App) pendingData(data map[string]any) error {
	keys, err := a.Settings.Differs(a.Cfg)
	if err != nil {
		return err
	}
	data["PendingRestart"] = len(keys) > 0
	data["CanRestart"] = a.Restart != nil
	return nil
}

func (a *App) configErr(w http.ResponseWriter, r *http.Request, status int, key string, detail error) {
	logger.Warn("ADMIN config %s: %v", key, detail)
	u := a.lang(w, r, nil)
	msg := a.I18n.T(u, key)
	if detail != nil {
		msg += ": " + detail.Error()
	}
	writeJSON(w, status, map[string]any{"ok": false, "error": msg, "key": key})
}

// configChanges reads the posted fields of one group. Blank plain fields clear
// the override, blank secrets are left as they are unless "clear_<key>" is set.
func configChanges(group string, in map[string]any) map[string]*string {
	changes := map[string]*string{}
	for _, f := range appconfig.InGroup(group) {
		switch {
		case f.Bool:
			if b, ok := in[f.Key].(bool); ok {
				v := "0"
				if b {
					v = "1"
				}
				changes[f.Key] = &v
			}
		case f.Secret:
			if clear, _ := in["clear_"+f.Key].(bool); clear {
				changes[f.Key] = nil
			} else if v, _ := in[f.Key].(string); strings.TrimSpace(v) != "" {
				v = strings.TrimSpace(v)
				changes[f.Key] = &v
			}
		default:
			v, _ := in[f.Key].(string)
			if v = strings.TrimSpace(v); v == "" {
				changes[f.Key] = nil
			} else {
				changes[f.Key] = &v
			}
		}
	}
	return changes
}

func validGroup(g string) bool {
	if g == "federation" {
		return true
	}
	for _, n := range appconfig.Groups {
		if n == g {
			return true
		}
	}
	return false
}

func (a *App) candidate(in map[string]any) (*config.Config, map[string]*string, string, error) {
	group, _ := in["group"].(string)
	if !validGroup(group) {
		return nil, nil, "", errBadRequest
	}
	changes := configChanges(group, in)
	c, err := a.Settings.Overlay(changes)
	if err != nil {
		return nil, nil, group, err
	}
	if !a.I18n.Has(c.DefaultLang) {
		return nil, nil, group, fmt.Errorf("the default language %q is not one of the available languages", c.DefaultLang)
	}
	if len([]rune(c.AppName)) == 0 || len([]rune(c.AppName)) > 40 {
		return nil, nil, group, fmt.Errorf("the app name must be 1 to 40 characters")
	}
	return c, changes, group, nil
}

func (a *App) apiAdminConfigSave(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in map[string]any
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	_, changes, group, err := a.candidate(in)
	if err != nil {
		if err == errBadRequest {
			a.fail(w, r, err)
		} else {
			a.configErr(w, r, http.StatusBadRequest, "err.config_invalid", err)
		}
		return
	}
	var keys []string
	for k, v := range changes {
		f, ok := appconfig.Lookup(k)
		if !ok {
			a.fail(w, r, errBadRequest)
			return
		}
		val := ""
		if v != nil {
			val = *v
		}
		if err := a.Settings.Set(f, val); err != nil {
			a.fail(w, r, fmt.Errorf("save setting %s: %w", k, err))
			return
		}
		keys = append(keys, k)
	}
	logger.Info("ADMIN uid=%d saved settings group=%s keys=%s (restart needed to apply)", admin.ID, group, strings.Join(keys, ","))
	a.okJSON(w, nil)
}

func (a *App) apiAdminConfigTest(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in map[string]any
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	c, _, group, err := a.candidate(in)
	if err != nil {
		if err == errBadRequest {
			a.fail(w, r, err)
		} else {
			a.configErr(w, r, http.StatusBadRequest, "err.config_invalid", err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	switch group {
	case "mail":
		to := admin.Email
		if v, _ := in["to"].(string); strings.TrimSpace(v) != "" {
			to = strings.TrimSpace(v)
		}
		if to == "" {
			a.configErr(w, r, http.StatusBadRequest, "err.test_no_email", nil)
			return
		}
		m := mailer.New(c)
		m.Record = a.Mail.Record
		subject := c.AppName + " test email"
		if err := m.Send(to, subject, "This is a test email from the admin settings. If you can read it, outgoing mail works."); err != nil {
			a.configErr(w, r, http.StatusBadGateway, "err.test_failed", err)
			return
		}
		logger.Info("ADMIN uid=%d sent a settings test email", admin.ID)
		a.okJSON(w, map[string]any{"detail": to})
	case "storage":
		if !c.S3.Configured() {
			a.configErr(w, r, http.StatusBadRequest, "err.test_not_configured", nil)
			return
		}
		s3, err := media.NewS3(c.S3)
		if err != nil {
			a.configErr(w, r, http.StatusBadRequest, "err.test_failed", err)
			return
		}
		if err := probeStore(ctx, s3); err != nil {
			a.configErr(w, r, http.StatusBadGateway, "err.test_failed", err)
			return
		}
		logger.Info("ADMIN uid=%d tested object storage settings ok", admin.ID)
		a.okJSON(w, map[string]any{"detail": c.S3.Bucket})
	case "telegram":
		bot := telegram.New(c.TelegramBot, c.TelegramName, "")
		if !bot.Configured() {
			a.configErr(w, r, http.StatusBadRequest, "err.test_not_configured", nil)
			return
		}
		name, err := bot.Check(ctx)
		if err != nil {
			a.configErr(w, r, http.StatusBadGateway, "err.test_failed", err)
			return
		}
		if !strings.EqualFold(name, bot.Username) {
			a.configErr(w, r, http.StatusBadRequest, "err.test_failed", fmt.Errorf("this token belongs to @%s, not @%s", name, bot.Username))
			return
		}
		logger.Info("ADMIN uid=%d tested the Telegram bot settings ok", admin.ID)
		a.okJSON(w, map[string]any{"detail": "@" + name})
	default:
		a.fail(w, r, errBadRequest)
	}
}

// probeStore writes, reads back and deletes a small object, which proves the
// credentials can do everything the app needs.
func probeStore(ctx context.Context, s media.Store) error {
	const key = "config-test/probe.txt"
	if err := s.Put(ctx, key, []byte("ok"), "text/plain"); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	obj, err := s.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read back: %w", err)
	}
	obj.Body.Close()
	if err := s.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

func (a *App) apiAdminRestart(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if a.Restart == nil {
		a.fail(w, r, errBadRequest)
		return
	}
	logger.Info("ADMIN uid=%d requested a restart", admin.ID)
	a.okJSON(w, nil)
	go func() {
		time.Sleep(500 * time.Millisecond)
		a.Restart()
	}()
}
