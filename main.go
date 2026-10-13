package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"tvt/appconfig"
	"tvt/assets"
	"tvt/auth"
	"tvt/config"
	"tvt/db"
	"tvt/federation"
	"tvt/handlers"
	"tvt/i18n"
	"tvt/logger"
	"tvt/mailer"
	"tvt/media"
	"tvt/notify"
	"tvt/store"
	"tvt/syncer"
	"tvt/telegram"
	"tvt/tmdb"
	"tvt/trakt"
	"tvt/tvdb"
	"tvt/tvmaze"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("config: %v", err)
	}
	d, err := db.Open(cfg.DBPath)
	if err != nil {
		logger.Fatal("database: %v", err)
	}
	defer d.Close()
	auth.ClientIPHeader = cfg.ClientIPHeader
	st := store.New(d)
	settings := appconfig.New(st, cfg.JWTSecret, cfg)
	if keys, err := settings.ImportEnv(); err != nil {
		logger.Error("could not import the settings from the environment: %v", err)
	} else if len(keys) > 0 {
		logger.Info("imported %d settings from the environment into Admin > Config (%s); the environment is no longer read for them, so those lines can be removed from the env file", len(keys), strings.Join(keys, ", "))
	}
	if err := settings.Apply(cfg); err != nil {
		logger.Error("settings saved in the admin UI were ignored, using the environment: %v", err)
	}
	if cfg.OperatorName == "" || cfg.PrivacyContact == "" {
		logger.Warn("The operator name and privacy contact are not both set (TVT_OPERATOR_NAME, TVT_PRIVACY_CONTACT or Admin > Config): the privacy page will not say who runs this site or how to reach them")
	}
	if cfg.AdminEmail != "" {
		if _, err := d.Exec(`UPDATE users SET is_admin = 1 WHERE email = ? AND email_verified = 1`, cfg.AdminEmail); err != nil {
			logger.Error("admin promotion for %s: %v", cfg.AdminEmail, err)
		}
	}
	tm := tmdb.New(cfg.TMDBKey)
	sy := syncer.New(st, tm)
	if !cfg.DisableTVmaze {
		sy.TVmaze = tvmaze.New()
	}
	if cfg.TVDBKey != "" {
		sy.TVDB = tvdb.New(cfg.TVDBKey)
	}
	if cfg.TraktClientID != "" {
		sy.Trakt = trakt.New(cfg.TraktClientID)
	}
	bundle, err := i18n.Load(assets.FS, "locales", cfg.DefaultLang)
	if err != nil {
		logger.Fatal("locales: %v", err)
	}
	ml := mailer.New(cfg)
	hook := hmac.New(sha256.New, []byte(cfg.JWTSecret))
	hook.Write([]byte("telegram-webhook"))
	bot := telegram.New(cfg.TelegramBot, cfg.TelegramName, hex.EncodeToString(hook.Sum(nil))[:48])
	vapidSubject := cfg.BaseURL
	if !strings.HasPrefix(vapidSubject, "https://") {
		vapidSubject = "mailto:" + strings.TrimPrefix(cfg.MailFrom, "mailto:")
	}
	nt := notify.New(st, ml, bot, bundle, notify.NewPusher(st, cfg.JWTSecret, vapidSubject), cfg.AppName, cfg.BaseURL)
	authSvc := auth.New(st, cfg.JWTSecret, cfg.CookieSecure)
	baseURL, perr := url.Parse(cfg.BaseURL)
	if perr != nil || baseURL.Hostname() == "" {
		logger.Fatal("TVT_BASE_URL %q is not a valid URL: %v", cfg.BaseURL, perr)
	}
	if err := authSvc.EnableWebAuthn(baseURL.Hostname(), cfg.BaseURL, cfg.AppName); err != nil {
		logger.Warn("passkeys disabled: %v (use a real domain or localhost as TVT_BASE_URL)", err)
	}
	var mediaStore media.Store = &media.Disk{Dir: filepath.Join(cfg.DataDir, "media")}
	if cfg.S3.Configured() {
		s3, err := media.NewS3(cfg.S3)
		if err != nil {
			logger.Fatal("s3 storage: %v", err)
		}
		mediaStore = s3
	}
	logger.Info("media storage: %s", mediaStore.Kind())
	restart := false
	app, err := handlers.New(cfg, st, authSvc, sy, tm, ml, nt, bot, mediaStore)
	if err != nil {
		logger.Fatal("app: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.Settings = settings
	app.Restart = func() {
		restart = true
		stop()
	}
	if cfg.Federation {
		fed, err := federation.New(st, nt, baseURL.Host, cfg.JWTSecret, cfg.FedInsecure, cfg.FedBlock, cfg.FedAllow)
		if err != nil {
			logger.Fatal("federation: %v", err)
		}
		app.Fed = fed
		go fed.Run(ctx)
		logger.Info("federation on as %s", baseURL.Host)
	}
	go sy.Run(ctx)
	nt.Start(ctx)
	go nt.RunJobs(ctx)
	go app.SetupTelegram()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		logger.Info("%s %s listening on %s (base %s)", cfg.AppName, logger.Version, cfg.Addr, cfg.BaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("server: %v", err)
		}
	}()
	<-ctx.Done()
	logger.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		logger.Error("shutdown: %v", err)
	}
	if restart {
		logger.Info("restart requested from the admin UI; exiting non-zero so the service manager starts the new settings")
		d.Close()
		os.Exit(1)
	}
}
