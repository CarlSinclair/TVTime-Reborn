package telegram

import shared "github.com/CarlSinclair/csuit-libs/telegram"

type (
	Bot           = shared.Bot
	LoginData     = shared.LoginData
	Button        = shared.Button
	Update        = shared.Update
	CallbackQuery = shared.CallbackQuery
	Message       = shared.Message
	Chat          = shared.Chat
	User          = shared.User
	Job           = shared.Job
	BotInfo       = shared.BotInfo
)

const (
	MaxCaption = shared.MaxCaption
)

var (
	New          = shared.New
	ParseCommand = shared.ParseCommand
	Do           = shared.Do
	Submit       = shared.Submit
	Use          = shared.Use
	For          = shared.For
	GetMe        = shared.GetMe
)

var (
	ErrNotConfigured = shared.ErrNotConfigured
	ErrBotBlocked    = shared.ErrBotBlocked
	ErrBadLogin      = shared.ErrBadLogin
)
