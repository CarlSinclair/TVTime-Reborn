package logger

import shared "github.com/CarlSinclair/csuit-libs/logger"

type (
	LogLevel    = shared.LogLevel
	AppLogger   = shared.AppLogger
	Entry       = shared.Entry
	RetryPolicy = shared.RetryPolicy
	Options     = shared.Options
)

const (
	LevelDebug = shared.LevelDebug
	LevelInfo  = shared.LevelInfo
	LevelWarn  = shared.LevelWarn
	LevelError = shared.LevelError
	LevelFatal = shared.LevelFatal
)

var (
	MigratedChatID             = shared.MigratedChatID
	ApplyChatMigration         = shared.ApplyChatMigration
	SetOnChatMigrated          = shared.SetOnChatMigrated
	NewContextWithTraceID      = shared.NewContextWithTraceID
	NewContextWithTraceIDValue = shared.NewContextWithTraceIDValue
	GetTraceID                 = shared.GetTraceID
	StructuredLog              = shared.StructuredLog
	LogWithContext             = shared.LogWithContext
	RememberLocation           = shared.RememberLocation
	RememberRequestLocation    = shared.RememberRequestLocation
	SetTimeZone                = shared.SetTimeZone
	Configure                  = shared.Configure
	Debug                      = shared.Debug
	Info                       = shared.Info
	Warn                       = shared.Warn
	Error                      = shared.Error
	Fatal                      = shared.Fatal
	NewError                   = shared.NewError
	Recent                     = shared.Recent
	Redact                     = shared.Redact
	ResetPersistent            = shared.ResetPersistent
	Retry                      = shared.Retry
	SendTelegramMessage        = shared.SendTelegramMessage
	PostTelegram               = shared.PostTelegram
	InitSentry                 = shared.InitSentry
	SentryLog                  = shared.SentryLog
	SentryBreadcrumb           = shared.SentryBreadcrumb
	SentryCapture              = shared.SentryCapture
	SentryRecover              = shared.SentryRecover
	SentryFlush                = shared.SentryFlush
	Setup                      = shared.Setup
	SetTelegramBaseOverride    = shared.SetTelegramBaseOverride
	SetTelegramBotToken        = shared.SetTelegramBotToken
	SetTelegramChatID          = shared.SetTelegramChatID
	SetTelegramBaseURL         = shared.SetTelegramBaseURL
	CurrentTelegramBaseURL     = shared.CurrentTelegramBaseURL
	TelegramBase               = shared.TelegramBase
	GetTelegramBotToken        = shared.GetTelegramBotToken
	GetTelegramChatID          = shared.GetTelegramChatID
	EscapeMarkdownV2           = shared.EscapeMarkdownV2
	SendTelegramMessageSync    = shared.SendTelegramMessageSync
	SendToUserSync             = shared.SendToUserSync
	EnqueueTelegramAlert       = shared.EnqueueTelegramAlert
	IsClientGone               = shared.IsClientGone
	Persistent                 = shared.Persistent
	RenderError                = shared.RenderError
	MissingKey                 = shared.MissingKey
	SetService                 = shared.SetService
	ServiceName                = shared.ServiceName
	CurrentVersion             = shared.CurrentVersion
)
