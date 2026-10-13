package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"tvt/media"
)

type Config struct {
	AppName        string
	Addr           string
	BaseURL        string
	DBPath         string
	JWTSecret      string
	TMDBKey        string
	CookieSecure   bool
	DefaultLang    string
	SMTPHost       string
	SMTPPort       int
	SMTPUser       string
	SMTPPass       string
	MailFrom       string
	DevLogMail     bool
	AdminEmail     string
	DataDir        string
	TelegramBot    string
	TelegramName   string
	ClientIPHeader string
	DisableTVmaze  bool
	ImageBase      string
	S3             media.S3Config
	TraktClientID  string
	TVDBKey        string
	Federation     bool
	FedInsecure    bool
	FedBlock       []string
	FedAllow       []string
	TVDBImageBase  string
	SourceURL      string
	MediaPublicURL string
	OperatorName   string
	PrivacyContact string
	ContactEmail   string
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envAny(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func Load() (*Config, error) {
	c := &Config{
		AppName:        env("TVT_APP_NAME", "TVT"),
		Addr:           env("TVT_ADDR", "127.0.0.1:9911"),
		BaseURL:        strings.TrimRight(env("TVT_BASE_URL", "http://127.0.0.1:9911"), "/"),
		DBPath:         env("TVT_DB", "tvt.db"),
		JWTSecret:      env("TVT_JWT_SECRET", ""),
		TMDBKey:        env("TVT_TMDB_KEY", ""),
		DefaultLang:    env("TVT_DEFAULT_LANG", "en"),
		SMTPHost:       env("TVT_SMTP_HOST", ""),
		MailFrom:       env("TVT_MAIL_FROM", ""),
		SMTPUser:       env("TVT_SMTP_USER", ""),
		SMTPPass:       env("TVT_SMTP_PASS", ""),
		DevLogMail:     env("TVT_DEV_LOG_MAIL", "") == "1",
		AdminEmail:     strings.ToLower(env("TVT_ADMIN_EMAIL", "")),
		DataDir:        env("TVT_DATA_DIR", "data"),
		TelegramBot:    env("TVT_TELEGRAM_BOT_TOKEN", ""),
		TelegramName:   env("TVT_TELEGRAM_BOT_USERNAME", ""),
		ClientIPHeader: env("TVT_CLIENT_IP_HEADER", "X-Forwarded-For"),
		DisableTVmaze:  env("TVT_DISABLE_TVMAZE", "") == "1",
		ImageBase:      strings.TrimRight(env("TVT_IMAGE_BASE", "https://image.tmdb.org/t/p"), "/"),
		TraktClientID:  env("TVT_TRAKT_CLIENT_ID", ""),
		TVDBKey:        env("TVT_TVDB_KEY", ""),
		Federation:     env("TVT_FEDERATION", "1") != "0",
		FedInsecure:    env("TVT_FEDERATION_INSECURE", "") == "1",
		FedBlock:       strings.Split(env("TVT_FEDERATION_BLOCK", ""), ","),
		FedAllow:       strings.Split(env("TVT_FEDERATION_ALLOW", ""), ","),
		TVDBImageBase:  strings.TrimRight(env("TVT_TVDB_IMAGE_BASE", "https://artworks.thetvdb.com"), "/"),
		MediaPublicURL: strings.TrimRight(env("TVT_MEDIA_PUBLIC_URL", ""), "/"),
		SourceURL:      env("TVT_SOURCE_URL", "https://github.com/CarlSinclair/tvt"),
		OperatorName:   env("TVT_OPERATOR_NAME", ""),
		PrivacyContact: env("TVT_PRIVACY_CONTACT", ""),
		ContactEmail:   env("TVT_CONTACT_EMAIL", ""),
		S3: media.S3Config{
			Endpoint: envAny("TVT_S3_ENDPOINT"), Region: envAny("TVT_S3_REGION"),
			Bucket: envAny("TVT_S3_BUCKET"), AccessKeyID: envAny("TVT_S3_ACCESS_KEY_ID"),
			SecretAccessKey: envAny("TVT_S3_SECRET_ACCESS_KEY"), Prefix: envAny("TVT_S3_PREFIX"),
		},
	}
	c.CookieSecure = strings.HasPrefix(c.BaseURL, "https://")
	port, err := strconv.Atoi(env("TVT_SMTP_PORT", "587"))
	if err != nil {
		return nil, fmt.Errorf("TVT_SMTP_PORT is not a number: %w", err)
	}
	c.SMTPPort = port
	c.S3.PublicURL = c.MediaPublicURL
	if err := c.ValidateStorage(); err != nil {
		return nil, err
	}
	if c.DevLogMail && c.CookieSecure {
		return nil, fmt.Errorf("TVT_DEV_LOG_MAIL logs login links and must not be enabled with an https base URL")
	}
	if strings.Contains(strings.ToLower(c.JWTSecret), "change-me") {
		return nil, fmt.Errorf("TVT_JWT_SECRET is still the example placeholder")
	}
	if len(c.JWTSecret) < 32 {
		return nil, fmt.Errorf("TVT_JWT_SECRET is missing or too short (%d chars, need >= 32)", len(c.JWTSecret))
	}
	return c, nil
}

// ValidateStorage checks the object-storage settings as a set: either all of
// them or none, with a public https domain for the bucket.
func (c *Config) ValidateStorage() error {
	c.S3.PublicURL = c.MediaPublicURL
	if c.MediaPublicURL != "" {
		u, err := url.Parse(c.MediaPublicURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("the public media URL must be an https URL with no query or fragment")
		}
		if !c.S3.Configured() {
			return fmt.Errorf("the public media URL needs S3 storage to be configured")
		}
	}
	if c.S3.Configured() && c.MediaPublicURL == "" {
		return fmt.Errorf("S3 storage is configured but the public media URL is not: a bucket is served directly from its public domain, so set it, or remove the S3 settings to keep files on local disk")
	}
	if c.S3.Partial() {
		return fmt.Errorf("S3 storage is partly configured: set the endpoint, bucket, access key id and secret together, or none of them")
	}
	return nil
}
