// Package appconfig lets an admin change operational settings from the web UI
// instead of the environment. A saved value overrides the matching environment
// variable; a blank one falls back to it. Secrets are stored sealed with the
// server secret. Settings are read once at start-up, so a change takes effect
// on the next restart.
package appconfig

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"tvt/config"
	"tvt/secretbox"
	"tvt/store"
)

const (
	keyPrefix   = "cfg."
	sealedLabel = "app-config"
	sealedMark  = "sealed:"
)

type Field struct {
	Key    string
	Group  string
	Secret bool
	Bool   bool
	get    func(*config.Config) string
	set    func(*config.Config, string) error
}

func str(get func(*config.Config) *string) (func(*config.Config) string, func(*config.Config, string) error) {
	return func(c *config.Config) string { return *get(c) }, func(c *config.Config, v string) error { *get(c) = v; return nil }
}

func field(key, group string, secret bool, p func(*config.Config) *string) Field {
	g, s := str(p)
	return Field{Key: key, Group: group, Secret: secret, get: g, set: s}
}

func boolField(key, group string, p func(*config.Config) *bool) Field {
	return Field{Key: key, Group: group, Bool: true,
		get: func(c *config.Config) string {
			if *p(c) {
				return "1"
			}
			return "0"
		},
		set: func(c *config.Config, v string) error {
			switch v {
			case "1":
				*p(c) = true
			case "0":
				*p(c) = false
			default:
				return fmt.Errorf("%q is not 1 or 0", v)
			}
			return nil
		}}
}

// Groups in the order the Config tab shows them.
var Groups = []string{"mail", "storage", "metadata", "telegram", "branding"}

var Fields = []Field{
	field("smtp_host", "mail", false, func(c *config.Config) *string { return &c.SMTPHost }),
	{Key: "smtp_port", Group: "mail",
		get: func(c *config.Config) string { return strconv.Itoa(c.SMTPPort) },
		set: func(c *config.Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("the SMTP port must be a number between 1 and 65535")
			}
			c.SMTPPort = n
			return nil
		}},
	field("smtp_user", "mail", false, func(c *config.Config) *string { return &c.SMTPUser }),
	field("smtp_pass", "mail", true, func(c *config.Config) *string { return &c.SMTPPass }),
	field("mail_from", "mail", false, func(c *config.Config) *string { return &c.MailFrom }),

	field("s3_endpoint", "storage", false, func(c *config.Config) *string { return &c.S3.Endpoint }),
	field("s3_region", "storage", false, func(c *config.Config) *string { return &c.S3.Region }),
	field("s3_bucket", "storage", false, func(c *config.Config) *string { return &c.S3.Bucket }),
	field("s3_access_key_id", "storage", false, func(c *config.Config) *string { return &c.S3.AccessKeyID }),
	field("s3_secret_access_key", "storage", true, func(c *config.Config) *string { return &c.S3.SecretAccessKey }),
	field("s3_prefix", "storage", false, func(c *config.Config) *string { return &c.S3.Prefix }),
	field("media_public_url", "storage", false, func(c *config.Config) *string { return &c.MediaPublicURL }),

	field("tmdb_key", "metadata", true, func(c *config.Config) *string { return &c.TMDBKey }),
	field("tvdb_key", "metadata", true, func(c *config.Config) *string { return &c.TVDBKey }),
	field("trakt_client_id", "metadata", false, func(c *config.Config) *string { return &c.TraktClientID }),
	boolField("disable_tvmaze", "metadata", func(c *config.Config) *bool { return &c.DisableTVmaze }),

	field("telegram_bot", "telegram", true, func(c *config.Config) *string { return &c.TelegramBot }),
	field("telegram_name", "telegram", false, func(c *config.Config) *string { return &c.TelegramName }),

	field("app_name", "branding", false, func(c *config.Config) *string { return &c.AppName }),
	field("default_lang", "branding", false, func(c *config.Config) *string { return &c.DefaultLang }),
	field("operator_name", "branding", false, func(c *config.Config) *string { return &c.OperatorName }),
	field("privacy_contact", "branding", false, func(c *config.Config) *string { return &c.PrivacyContact }),
	field("source_url", "branding", false, func(c *config.Config) *string { return &c.SourceURL }),

	boolField("federation", "federation", func(c *config.Config) *bool { return &c.Federation }),
}

func Lookup(key string) (Field, bool) {
	for _, f := range Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

func InGroup(group string) []Field {
	var out []Field
	for _, f := range Fields {
		if f.Group == group {
			out = append(out, f)
		}
	}
	return out
}

// Manager reads and writes the saved overrides. Env is the configuration as the
// environment alone defined it, kept so the UI can show what blank falls back to.
type Manager struct {
	St     *store.Store
	Secret string
	Env    config.Config
}

func New(st *store.Store, secret string, env *config.Config) *Manager {
	return &Manager{St: st, Secret: secret, Env: *env}
}

// Saved returns the override stored for a key, if any.
func (m *Manager) Saved(f Field) (string, bool, error) {
	raw := m.St.KVGet(keyPrefix + f.Key)
	if raw == "" {
		return "", false, nil
	}
	if f.Secret {
		if !strings.HasPrefix(raw, sealedMark) {
			return "", false, fmt.Errorf("saved value of %s is not sealed", f.Key)
		}
		plain, err := secretbox.Open(m.Secret, sealedLabel, strings.TrimPrefix(raw, sealedMark))
		if err != nil {
			return "", false, fmt.Errorf("open saved %s: %w", f.Key, err)
		}
		return plain, true, nil
	}
	return raw, true, nil
}

// Set stores an override; an empty value removes it so the environment applies.
func (m *Manager) Set(f Field, value string) error {
	if value == "" {
		return m.St.KVDelete(keyPrefix + f.Key)
	}
	if f.Secret {
		sealed, err := secretbox.Seal(m.Secret, sealedLabel, value)
		if err != nil {
			return fmt.Errorf("seal %s: %w", f.Key, err)
		}
		value = sealedMark + sealed
	}
	return m.St.KVSet(keyPrefix+f.Key, value)
}

// Overlay applies the saved overrides, plus the given pending changes (a nil
// pointer clears one), to a copy of the environment configuration.
func (m *Manager) Overlay(changes map[string]*string) (*config.Config, error) {
	c := m.Env
	c.FedBlock = append([]string(nil), m.Env.FedBlock...)
	c.FedAllow = append([]string(nil), m.Env.FedAllow...)
	for _, f := range Fields {
		v, ok, err := m.Saved(f)
		if err != nil {
			return nil, err
		}
		if ch, has := changes[f.Key]; has {
			if ch == nil {
				ok = false
			} else {
				v, ok = *ch, *ch != ""
			}
		}
		if !ok {
			continue
		}
		if err := f.set(&c, v); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Key, err)
		}
	}
	if err := c.ValidateStorage(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Apply copies the saved overrides onto the running configuration at start-up.
// A saved set that does not validate is ignored, so a bad value can never lock
// the admin out of the page that would fix it.
func (m *Manager) Apply(run *config.Config) error {
	c, err := m.Overlay(nil)
	if err != nil {
		return err
	}
	*run = *c
	return nil
}

// Differs lists the keys whose saved value differs from what is running now.
func (m *Manager) Differs(run *config.Config) ([]string, error) {
	c, err := m.Overlay(nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range Fields {
		if f.get(c) != f.get(run) {
			out = append(out, f.Key)
		}
	}
	return out, nil
}

var ErrUnknownField = errors.New("appconfig: unknown field")

// Value is a field as the Config tab shows it.
type Value struct {
	Key    string
	Secret bool
	Bool   bool
	// Text is the saved value for plain fields; secrets never leave the server.
	Text string
	// Saved is true when an override is stored; Set is true when a secret has a value.
	Saved bool
	// Fallback is the environment's value for plain fields, shown as a hint.
	Fallback string
	EnvSet   bool
}

func (m *Manager) Values(group string) ([]Value, error) {
	var out []Value
	for _, f := range InGroup(group) {
		v, ok, err := m.Saved(f)
		if err != nil {
			return nil, err
		}
		val := Value{Key: f.Key, Secret: f.Secret, Bool: f.Bool, Saved: ok}
		envVal := f.get(&m.Env)
		val.EnvSet = envVal != "" && !(f.Bool && envVal == "0")
		if !f.Secret {
			val.Fallback = envVal
			switch {
			case ok:
				val.Text = v
			case f.Bool:
				val.Text = envVal
			}
		}
		out = append(out, val)
	}
	return out, nil
}
