package telegram

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"tvt/logger"
)

var (
	ErrNotConfigured = errors.New("telegram bot is not configured")
	ErrBotBlocked    = errors.New("telegram: bot was blocked by the user or the chat is gone")
	ErrBadLogin      = errors.New("telegram login data is invalid or expired")
)

const apiBase = "https://api.telegram.org"

type Bot struct {
	Token    string
	Username string
	Secret   string
	API      string
	http     *http.Client

	seenMu sync.Mutex
	seen   map[string]time.Time

	paceMu   sync.Mutex
	nextAny  time.Time
	nextChat map[string]time.Time
}

const (
	globalGap     = 500 * time.Millisecond
	perChatGap    = 1100 * time.Millisecond
	maxRetryAfter = 30 * time.Second
)

func New(token, username, webhookSecret string) *Bot {
	return &Bot{
		Token: strings.TrimSpace(token), Username: strings.TrimPrefix(strings.TrimSpace(username), "@"), Secret: webhookSecret,
		API: apiBase, http: &http.Client{Timeout: 15 * time.Second}, seen: map[string]time.Time{}, nextChat: map[string]time.Time{},
	}
}

func (b *Bot) Configured() bool { return b != nil && b.Token != "" && b.Username != "" }

func (b *Bot) BotID() string {
	if i := strings.IndexByte(b.Token, ':'); i > 0 {
		return b.Token[:i]
	}
	return ""
}

func (b *Bot) DeepLink(startToken string) string {
	return "tg://resolve?domain=" + url.QueryEscape(b.Username) + "&start=" + url.QueryEscape(startToken)
}

type LoginData struct {
	ID        int64
	Username  string
	FirstName string
	LastName  string
	AuthDate  time.Time
}

func (l LoginData) DisplayName() string {
	return strings.TrimSpace(l.FirstName + " " + l.LastName)
}

// VerifyLogin checks a Telegram Login Widget payload: the HMAC over the sorted
// fields keyed by SHA-256(bot token), a freshness window, and a replay cache so
// the same signed payload cannot be redeemed twice inside that window.
func (b *Bot) VerifyLogin(vals url.Values) (LoginData, error) {
	var out LoginData
	if !b.Configured() {
		return out, ErrNotConfigured
	}
	got := strings.TrimSpace(vals.Get("hash"))
	if got == "" {
		return out, ErrBadLogin
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		if k == "hash" || k == "" || k == "action" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+vals.Get(k))
	}
	secret := sha256.Sum256([]byte(b.Token))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(parts, "\n")))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(strings.ToLower(got))) != 1 {
		return out, ErrBadLogin
	}
	ad, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if err != nil || ad == 0 {
		return out, ErrBadLogin
	}
	out.AuthDate = time.Unix(ad, 0)
	if time.Since(out.AuthDate) > time.Hour || out.AuthDate.After(time.Now().Add(5*time.Minute)) {
		return out, ErrBadLogin
	}
	id, err := strconv.ParseInt(vals.Get("id"), 10, 64)
	if err != nil || id <= 0 {
		return out, ErrBadLogin
	}
	b.seenMu.Lock()
	defer b.seenMu.Unlock()
	now := time.Now()
	for k, t := range b.seen {
		if now.Sub(t) > 2*time.Hour {
			delete(b.seen, k)
		}
	}
	if _, dup := b.seen[want]; dup {
		return out, ErrBadLogin
	}
	b.seen[want] = now
	out.ID, out.Username, out.FirstName, out.LastName = id, vals.Get("username"), vals.Get("first_name"), vals.Get("last_name")
	return out, nil
}

func (b *Bot) VerifyWebhook(r *http.Request) bool {
	if b.Secret == "" {
		return false
	}
	got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(b.Secret)) == 1
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

type rateLimitedError struct{ wait time.Duration }

func (e rateLimitedError) Error() string {
	return fmt.Sprintf("telegram rate limited, retry after %s", e.wait)
}

func (b *Bot) call(ctx context.Context, method string, payload any) error {
	if !b.Configured() {
		return ErrNotConfigured
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram %s marshal: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.API+"/bot"+b.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, scrub(err, b.Token))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out apiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("telegram %s: bad response status %d", method, resp.StatusCode)
	}
	if !out.OK {
		if out.ErrorCode == http.StatusForbidden || (out.ErrorCode == http.StatusBadRequest && strings.Contains(out.Description, "chat not found")) {
			return fmt.Errorf("%w: %s", ErrBotBlocked, out.Description)
		}
		if out.ErrorCode == http.StatusTooManyRequests {
			return rateLimitedError{wait: time.Duration(out.Parameters.RetryAfter) * time.Second}
		}
		return fmt.Errorf("telegram %s: %d %s", method, out.ErrorCode, out.Description)
	}
	return nil
}

// pace blocks until a message to chat may be sent: at most two messages a
// second overall and one a second per chat, which keeps clear of Telegram's
// limits.
func (b *Bot) pace(ctx context.Context, chat string) error {
	b.paceMu.Lock()
	now := time.Now()
	at := b.nextAny
	if c := b.nextChat[chat]; c.After(at) {
		at = c
	}
	if at.Before(now) {
		at = now
	}
	b.nextAny = at.Add(globalGap)
	b.nextChat[chat] = at.Add(perChatGap)
	if len(b.nextChat) > 5000 {
		for k, v := range b.nextChat {
			if v.Before(now) {
				delete(b.nextChat, k)
			}
		}
	}
	b.paceMu.Unlock()
	t := time.NewTimer(time.Until(at))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func scrub(err error, token string) error {
	if token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "<token>"))
}

func (b *Bot) SendMessage(ctx context.Context, chatID, text string) error {
	if len(text) > 4000 {
		text = text[:4000]
	}
	payload := map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true}
	if err := b.pace(ctx, chatID); err != nil {
		return err
	}
	err := b.call(ctx, "sendMessage", payload)
	var rl rateLimitedError
	if errors.As(err, &rl) && rl.wait > 0 && rl.wait <= maxRetryAfter {
		logger.Warn("TELEGRAM rate limited chat=%s, retrying in %s", chatID, rl.wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(rl.wait):
		}
		return b.call(ctx, "sendMessage", payload)
	}
	return err
}

func (b *Bot) SetWebhook(ctx context.Context, webhookURL string) error {
	return b.call(ctx, "setWebhook", map[string]any{
		"url": webhookURL, "secret_token": b.Secret, "allowed_updates": []string{"message"}, "drop_pending_updates": false,
	})
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Chat      Chat   `json:"chat"`
	From      *User  `json:"from"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
}

// ParseCommand splits "/start abc" or "/start@MyBot abc" into command and arg.
func ParseCommand(text string) (cmd, arg string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", ""
	}
	cmd = fields[0]
	if i := strings.IndexByte(cmd, '@'); i >= 0 {
		cmd = cmd[:i]
	}
	if len(fields) > 1 {
		arg = fields[1]
	}
	return strings.ToLower(cmd), arg
}

// Check asks Telegram who the token belongs to, so an admin can test the
// settings before relying on them. It returns the bot's username.
func (b *Bot) Check(ctx context.Context) (string, error) {
	if b == nil || b.Token == "" {
		return "", ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.API+"/bot"+b.Token+"/getMe", nil)
	if err != nil {
		return "", fmt.Errorf("telegram getMe request: %w", err)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("telegram getMe: %w", scrub(err, b.Token))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("telegram getMe: bad response status %d", resp.StatusCode)
	}
	if !out.OK {
		return "", fmt.Errorf("telegram getMe: %s", out.Description)
	}
	return out.Result.Username, nil
}
