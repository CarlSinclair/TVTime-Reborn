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
)

const (
	MaxCaption = shared.MaxCaption
)

var (
	New          = shared.New
	ParseCommand = shared.ParseCommand
)

var (
	ErrNotConfigured = shared.ErrNotConfigured
	ErrBotBlocked    = shared.ErrBotBlocked
	ErrBadLogin      = shared.ErrBadLogin
)
