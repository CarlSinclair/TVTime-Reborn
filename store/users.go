package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type User struct {
	ID            int64
	Email         string
	Username      string
	DisplayName   string
	Bio           string
	AvatarPath    string
	BannerPath    string
	PasswordHash  string
	EmailVerified bool
	Locale        string
	Timezone      string
	ProfilePublic bool
	IsAdmin       bool
	IsDeactivated bool
	// SelfDeactivated is set when the person deactivated their own account,
	// which signing in again lifts; an admin's suspension is not lifted that way.
	SelfDeactivated  bool
	IsBanned         bool
	TOTPSecretEnc    string
	TOTPEnabled      bool
	TOTPLastStep     int64
	TelegramID       int64
	TelegramUsername string
	TelegramChatID   string
	NotifyInApp      bool
	NotifyPush       bool
	NotifyEmail      bool
	NotifyTelegram   bool
	NotifyOffsetMin  int
	PendingEmail     string
	ShareActivity    bool
	RemoteHost       string
	CreatedAt        string
}

func (u *User) Name() string {
	if strings.TrimSpace(u.DisplayName) != "" {
		return u.DisplayName
	}
	return u.Username
}

// CanComment is true once the account has proven control of an email or a
// Telegram identity (both are costly enough to fake to keep drive-by spam
// down) and the account is not banned. A ban makes an account read-only.
func (u *User) CanComment() bool { return !u.IsBanned && (u.EmailVerified || u.TelegramID != 0) }

func (u *User) HasPassword() bool { return u.PasswordHash != "" }

func (u *User) Location() *time.Location {
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil || u.Timezone == "" {
		return time.UTC
	}
	return loc
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_]{3,20}$`)

func ValidUsername(u string) bool { return usernameRe.MatchString(u) }

const userCols = `id, COALESCE(email,''), username, display_name, bio, avatar_path, banner_path, password_hash, email_verified, locale, timezone,
  profile_public, is_admin, is_deactivated, is_banned, totp_secret_enc, totp_enabled, totp_last_step, COALESCE(telegram_id,0), telegram_username, telegram_chat_id,
  notify_in_app, notify_push, notify_email, notify_telegram, notify_offset_min, pending_email, share_activity, remote_host, created_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var ev, pub, adm, deact, ban, te, nIn, nPush, nMail, nTg, share int
	if err := row.Scan(&u.ID, &u.Email, &u.Username, &u.DisplayName, &u.Bio, &u.AvatarPath, &u.BannerPath, &u.PasswordHash, &ev, &u.Locale, &u.Timezone,
		&pub, &adm, &deact, &ban, &u.TOTPSecretEnc, &te, &u.TOTPLastStep, &u.TelegramID, &u.TelegramUsername, &u.TelegramChatID,
		&nIn, &nPush, &nMail, &nTg, &u.NotifyOffsetMin, &u.PendingEmail, &share, &u.RemoteHost, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.EmailVerified, u.ProfilePublic, u.IsAdmin, u.IsDeactivated, u.SelfDeactivated, u.IsBanned, u.TOTPEnabled = ev == 1, pub == 1, adm == 1, deact != 0, deact == 2, ban == 1, te == 1
	u.NotifyInApp, u.NotifyPush, u.NotifyEmail, u.NotifyTelegram = nIn == 1, nPush == 1, nMail == 1, nTg == 1
	u.ShareActivity = share == 1
	return &u, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) CreateUser(email, username, passwordHash, locale string, admin bool) (*User, error) {
	if held, err := s.nameHeldByOther(username, 0); err != nil {
		return nil, err
	} else if held {
		return nil, ErrUsernameTaken
	}
	adm := 0
	if admin {
		adm = 1
	}
	res, err := s.DB.Exec(`INSERT INTO users (email, username, password_hash, locale, is_admin, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		nullIfEmpty(email), username, passwordHash, locale, adm, Now())
	if err != nil {
		if isUnique(err, "users.email") {
			return nil, ErrEmailTaken
		}
		if isUnique(err, "users.username") {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("insert user email=%s username=%s: %w", email, username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("user last insert id email=%s: %w", email, err)
	}
	return s.UserByID(id)
}

var nonUserChars = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// CreateTelegramUser creates a password-less account for a Telegram identity,
// deriving a free username from the Telegram handle (or id) and suffixing
// digits until it is unique.
func (s *Store) CreateTelegramUser(tgID int64, tgUsername, displayName, locale string) (*User, error) {
	base := nonUserChars.ReplaceAllString(tgUsername, "")
	if len(base) < 3 {
		base = "tg" + strconv.FormatInt(tgID, 10)
	}
	if len(base) > 16 {
		base = base[:16]
	}
	if ReservedUsername(base) {
		base += "_"
	}
	name := base
	for i := 0; i < 50; i++ {
		res, err := s.DB.Exec(`INSERT INTO users (username, display_name, locale, telegram_id, telegram_username, created_at) VALUES (?,?,?,?,?,?)`,
			name, displayName, locale, tgID, tgUsername, Now())
		if err == nil {
			id, idErr := res.LastInsertId()
			if idErr != nil {
				return nil, fmt.Errorf("telegram user last insert id tg=%d: %w", tgID, idErr)
			}
			return s.UserByID(id)
		}
		if isUnique(err, "users.telegram_id") {
			return s.UserByTelegramID(tgID)
		}
		if !isUnique(err, "users.username") {
			return nil, fmt.Errorf("insert telegram user tg=%d: %w", tgID, err)
		}
		name = fmt.Sprintf("%s%d", base, i+2)
		if len(name) > 20 {
			name = name[:20]
		}
	}
	return nil, fmt.Errorf("could not allocate a username for telegram id %d", tgID)
}

func (s *Store) UserByID(id int64) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Store) UserByEmail(email string) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE email = ?`, email))
}

func (s *Store) UserByUsername(name string) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, name))
}

func (s *Store) UserByTelegramID(id int64) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE telegram_id = ?`, id))
}

func (s *Store) UserByTelegramChat(chatID string) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE telegram_chat_id = ? AND telegram_chat_id != ''`, chatID))
}

func (s *Store) Admins() ([]User, error) {
	rows, err := s.DB.Query(`SELECT ` + userCols + ` FROM users WHERE is_admin = 1 AND is_deactivated = 0`)
	if err != nil {
		return nil, fmt.Errorf("list admins: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *Store) exec(what string, userID int64, q string, args ...any) error {
	if _, err := s.DB.Exec(q, args...); err != nil {
		return fmt.Errorf("%s uid=%d: %w", what, userID, err)
	}
	return nil
}

func (s *Store) SetPassword(userID int64, hash string) error {
	return s.exec("set password", userID, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
}

func (s *Store) SetEmailVerified(userID int64) error {
	return s.exec("set email verified", userID, `UPDATE users SET email_verified = 1 WHERE id = ?`, userID)
}

func (s *Store) SetLocale(userID int64, locale string) error {
	return s.exec("set locale", userID, `UPDATE users SET locale = ? WHERE id = ?`, locale, userID)
}

func (s *Store) SetTimezone(userID int64, tz string) error {
	return s.exec("set timezone", userID, `UPDATE users SET timezone = ? WHERE id = ?`, tz, userID)
}

func (s *Store) AddEmail(userID int64, email string) error {
	res, err := s.DB.Exec(`UPDATE users SET email = ?, email_verified = 0 WHERE id = ? AND (email IS NULL OR email = '')`, email, userID)
	if err != nil {
		if isUnique(err, "users.email") {
			return ErrEmailTaken
		}
		return fmt.Errorf("add email uid=%d: %w", userID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrForbidden
	}
	return nil
}

// SetPendingEmail records an address waiting for confirmation. It fails with
// ErrEmailTaken when someone already owns it or is waiting to.
func (s *Store) SetPendingEmail(userID int64, email string) error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE id <> ? AND (email = ? OR pending_email = ?)`, userID, email, email).Scan(&n); err != nil {
		return fmt.Errorf("check pending email uid=%d: %w", userID, err)
	}
	if n > 0 {
		return ErrEmailTaken
	}
	return s.exec("set pending email", userID, `UPDATE users SET pending_email = ? WHERE id = ?`, email, userID)
}

// ConfirmEmailChange swaps in the pending address, already proven by the link,
// and returns the previous one so it can be told.
func (s *Store) ConfirmEmailChange(userID int64) (oldEmail, newEmail string, err error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return "", "", fmt.Errorf("begin email change uid=%d: %w", userID, err)
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`SELECT COALESCE(email,''), pending_email FROM users WHERE id = ?`, userID).Scan(&oldEmail, &newEmail); err != nil {
		return "", "", fmt.Errorf("read email change uid=%d: %w", userID, err)
	}
	if newEmail == "" {
		return "", "", ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE users SET email = ?, email_verified = 1, pending_email = '' WHERE id = ?`, newEmail, userID); err != nil {
		if isUnique(err, "users.email") {
			return "", "", ErrEmailTaken
		}
		return "", "", fmt.Errorf("apply email change uid=%d: %w", userID, err)
	}
	return oldEmail, newEmail, tx.Commit()
}

func (s *Store) UpdateProfile(userID int64, displayName, bio string, public, shareActivity bool) error {
	p, sh := 0, 0
	if public {
		p = 1
	}
	if shareActivity {
		sh = 1
	}
	return s.exec("update profile", userID, `UPDATE users SET display_name = ?, bio = ?, profile_public = ?, share_activity = ? WHERE id = ?`, displayName, bio, p, sh, userID)
}

func (s *Store) SetAvatar(userID int64, path string) error {
	return s.exec("set avatar", userID, `UPDATE users SET avatar_path = ? WHERE id = ?`, path, userID)
}

func (s *Store) SetBanner(userID int64, path string) error {
	return s.exec("set banner", userID, `UPDATE users SET banner_path = ? WHERE id = ?`, path, userID)
}

func (s *Store) SetNotifyFlags(userID int64, inApp, push, email, telegram bool) error {
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	return s.exec("set notify flags", userID, `UPDATE users SET notify_in_app = ?, notify_push = ?, notify_email = ?, notify_telegram = ? WHERE id = ?`,
		b(inApp), b(push), b(email), b(telegram), userID)
}

// SetDeactivated suspends an account on an admin's say-so (or lifts that): it
// cannot sign in, its sessions end and it is hidden from everyone else.
func (s *Store) SetDeactivated(userID int64, deactivated bool) error {
	v := 0
	if deactivated {
		v = 1
	}
	if err := s.exec("set deactivated", userID, `UPDATE users SET is_deactivated = ? WHERE id = ?`, v, userID); err != nil {
		return err
	}
	if deactivated {
		return s.exec("revoke refresh tokens", userID, `DELETE FROM refresh_tokens WHERE user_id = ?`, userID)
	}
	return s.RestoreComments(userID)
}

// DeactivateSelf is deactivation the person chose: the same as a suspension,
// except that signing in again lifts it.
func (s *Store) DeactivateSelf(userID int64) error {
	if err := s.exec("deactivate self", userID, `UPDATE users SET is_deactivated = 2 WHERE id = ?`, userID); err != nil {
		return err
	}
	return s.exec("revoke refresh tokens", userID, `DELETE FROM refresh_tokens WHERE user_id = ?`, userID)
}

// Reactivate lifts a self-deactivation and brings back the comments that were
// hidden with it.
func (s *Store) Reactivate(userID int64) error {
	if err := s.exec("reactivate", userID, `UPDATE users SET is_deactivated = 0 WHERE id = ? AND is_deactivated = 2`, userID); err != nil {
		return err
	}
	return s.RestoreComments(userID)
}

// SetBanned makes an account read-only: it can sign in and read but cannot
// comment or post.
func (s *Store) SetBanned(userID int64, banned bool) error {
	v := 0
	if banned {
		v = 1
	}
	if err := s.exec("set banned", userID, `UPDATE users SET is_banned = ? WHERE id = ?`, v, userID); err != nil {
		return err
	}
	if !banned {
		return s.RestoreComments(userID)
	}
	return nil
}

func (s *Store) DeleteUser(userID int64) error {
	return s.exec("delete user", userID, `DELETE FROM users WHERE id = ?`, userID)
}

var ErrTelegramTaken = errors.New("telegram account already linked to another user")

func (s *Store) LinkTelegram(userID, tgID int64, tgUsername string) error {
	res, err := s.DB.Exec(`UPDATE users SET telegram_id = ?, telegram_username = ? WHERE id = ?`, tgID, tgUsername, userID)
	if err != nil {
		if isUnique(err, "users.telegram_id") {
			return ErrTelegramTaken
		}
		return fmt.Errorf("link telegram uid=%d tg=%d: %w", userID, tgID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UnlinkTelegram(userID int64) error {
	return s.exec("unlink telegram", userID, `UPDATE users SET telegram_id = NULL, telegram_username = '', telegram_chat_id = '' WHERE id = ?`, userID)
}

func (s *Store) SetTelegramChat(userID int64, chatID string) error {
	return s.exec("set telegram chat", userID, `UPDATE users SET telegram_chat_id = ? WHERE id = ?`, chatID, userID)
}

func (s *Store) SetTelegramChatClearOthers(userID int64, chatID string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin set telegram chat uid=%d: %w", userID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET telegram_chat_id = '' WHERE telegram_chat_id = ? AND id != ?`, chatID, userID); err != nil {
		return fmt.Errorf("clear duplicate telegram chat uid=%d: %w", userID, err)
	}
	if _, err := tx.Exec(`UPDATE users SET telegram_chat_id = ? WHERE id = ?`, chatID, userID); err != nil {
		return fmt.Errorf("set telegram chat uid=%d: %w", userID, err)
	}
	return tx.Commit()
}

func (s *Store) SetTOTPSecret(userID int64, enc string) error {
	return s.exec("set totp secret", userID, `UPDATE users SET totp_secret_enc = ?, totp_enabled = 0, totp_last_step = 0 WHERE id = ?`, enc, userID)
}

func (s *Store) EnableTOTP(userID, step int64, recoveryHashes []string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin enable totp uid=%d: %w", userID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET totp_enabled = 1, totp_last_step = ? WHERE id = ?`, step, userID); err != nil {
		return fmt.Errorf("enable totp uid=%d: %w", userID, err)
	}
	if _, err := tx.Exec(`DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("clear recovery codes uid=%d: %w", userID, err)
	}
	for _, h := range recoveryHashes {
		if _, err := tx.Exec(`INSERT INTO recovery_codes (user_id, code_hash) VALUES (?, ?)`, userID, h); err != nil {
			return fmt.Errorf("store recovery code uid=%d: %w", userID, err)
		}
	}
	return tx.Commit()
}

func (s *Store) DisableTOTP(userID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin disable totp uid=%d: %w", userID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET totp_secret_enc = '', totp_enabled = 0, totp_last_step = 0 WHERE id = ?`, userID); err != nil {
		return fmt.Errorf("disable totp uid=%d: %w", userID, err)
	}
	if _, err := tx.Exec(`DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("clear recovery codes uid=%d: %w", userID, err)
	}
	return tx.Commit()
}

// ClaimTOTPStep records the accepted time step only if it is newer than the
// last one used, so a code cannot be replayed within its validity window.
func (s *Store) ClaimTOTPStep(userID, step int64) (bool, error) {
	res, err := s.DB.Exec(`UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, userID, step)
	if err != nil {
		return false, fmt.Errorf("claim totp step uid=%d: %w", userID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) ConsumeRecoveryCode(userID int64, hash string) (bool, error) {
	res, err := s.DB.Exec(`UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`, Now(), userID, hash)
	if err != nil {
		return false, fmt.Errorf("consume recovery code uid=%d: %w", userID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) RecoveryCodesLeft(userID int64) (int, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count recovery codes uid=%d: %w", userID, err)
	}
	return n, nil
}

func (s *Store) CreateTelegramLinkToken(userID int64, hash string, expires time.Time) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin telegram link token uid=%d: %w", userID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM telegram_link_tokens WHERE user_id = ? OR expires_at < ?`, userID, Now()); err != nil {
		return fmt.Errorf("prune telegram link tokens uid=%d: %w", userID, err)
	}
	if _, err := tx.Exec(`INSERT INTO telegram_link_tokens (token_hash, user_id, expires_at) VALUES (?,?,?)`, hash, userID, Ts(expires)); err != nil {
		return fmt.Errorf("store telegram link token uid=%d: %w", userID, err)
	}
	return tx.Commit()
}

// ConsumeTelegramLinkToken returns the owner of a valid token and deletes it.
func (s *Store) ConsumeTelegramLinkToken(hash string) (int64, error) {
	var uid int64
	var exp string
	err := s.DB.QueryRow(`SELECT user_id, expires_at FROM telegram_link_tokens WHERE token_hash = ?`, hash).Scan(&uid, &exp)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("read telegram link token: %w", err)
	}
	res, err := s.DB.Exec(`DELETE FROM telegram_link_tokens WHERE token_hash = ?`, hash)
	if err != nil {
		return 0, fmt.Errorf("consume telegram link token: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 || ParseTs(exp).Before(time.Now()) {
		return 0, ErrNotFound
	}
	return uid, nil
}

func (s *Store) SetAdmin(userID int64) error {
	return s.exec("set admin", userID, `UPDATE users SET is_admin = 1 WHERE id = ?`, userID)
}

var reservedUsernames = map[string]bool{
	"deleted": true,
	"admin":   true, "administrator": true, "root": true, "support": true, "moderator": true, "mod": true, "system": true,
	"staff": true, "help": true, "security": true, "official": true, "tvt": true, "api": true, "null": true, "undefined": true,
}

// ReservedUsername blocks names that could pass for staff or collide with routes.
func ReservedUsername(name string) bool { return reservedUsernames[strings.ToLower(name)] }

const MaxNotifyOffsetMin = 7 * 24 * 60

func (s *Store) SetNotifyTiming(userID int64, offsetMin int) error {
	if offsetMin < -MaxNotifyOffsetMin || offsetMin > MaxNotifyOffsetMin {
		return fmt.Errorf("invalid notification offset %d", offsetMin)
	}
	return s.exec("set notify timing", userID, `UPDATE users SET notify_offset_min = ? WHERE id = ?`, offsetMin, userID)
}

// UsernameHold is how long a name stays reserved for, and redirects to, the
// person who gave it up.
const UsernameHold = 14 * 24 * time.Hour

// UsernameRenameGap is the least time between two changes by the same person.
const UsernameRenameGap = 30 * 24 * time.Hour

var ErrUsernameCooldown = errors.New("username changed too recently")

// nameHeldByOther says whether a recently released name is still reserved for
// someone other than exceptUserID.
func (s *Store) nameHeldByOther(name string, exceptUserID int64) (bool, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM username_history WHERE old_name = ? AND user_id <> ? AND changed_at > ?`,
		name, exceptUserID, Ts(time.Now().Add(-UsernameHold))).Scan(&n); err != nil {
		return false, fmt.Errorf("check held username %s: %w", name, err)
	}
	return n > 0, nil
}

// LastUsernameChange is when the person last changed their username, or the
// zero time if they never did.
func (s *Store) LastUsernameChange(userID int64) (time.Time, error) {
	var at sql.NullString
	if err := s.DB.QueryRow(`SELECT MAX(changed_at) FROM username_history WHERE user_id = ?`, userID).Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("last username change uid=%d: %w", userID, err)
	}
	if !at.Valid {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", at.String)
	if err != nil {
		return time.Time{}, nil
	}
	return t, nil
}

// RenameUser gives a person a new username and keeps the old one held for
// UsernameHold. enforceGap applies the self-service limit on how often a name
// may change. It returns the previous name.
func (s *Store) RenameUser(userID int64, newName string, enforceGap bool) (string, error) {
	u, err := s.UserByID(userID)
	if err != nil {
		return "", err
	}
	if u.Username == newName {
		return u.Username, nil
	}
	caseOnly := strings.EqualFold(u.Username, newName)
	if !caseOnly {
		held, err := s.nameHeldByOther(newName, userID)
		if err != nil {
			return "", err
		}
		if held {
			return "", ErrUsernameTaken
		}
		if enforceGap {
			last, err := s.LastUsernameChange(userID)
			if err != nil {
				return "", err
			}
			if !last.IsZero() && time.Since(last) < UsernameRenameGap {
				return "", ErrUsernameCooldown
			}
		}
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return "", fmt.Errorf("begin rename uid=%d: %w", userID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET username = ? WHERE id = ?`, newName, userID); err != nil {
		if isUnique(err, "users.username") {
			return "", ErrUsernameTaken
		}
		return "", fmt.Errorf("rename uid=%d: %w", userID, err)
	}
	if !caseOnly {
		if _, err := tx.Exec(`DELETE FROM username_history WHERE changed_at < ?`, Ts(time.Now().Add(-UsernameHold))); err != nil {
			return "", fmt.Errorf("prune username history: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO username_history (user_id, old_name, changed_at) VALUES (?,?,?)`, userID, u.Username, Now()); err != nil {
			return "", fmt.Errorf("record username change uid=%d: %w", userID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit rename uid=%d: %w", userID, err)
	}
	return u.Username, nil
}

// UsernameRedirect finds who now has a name that was given up recently.
func (s *Store) UsernameRedirect(old string) (string, bool, error) {
	var cur string
	err := s.DB.QueryRow(`SELECT u.username FROM username_history h JOIN users u ON u.id = h.user_id
	  WHERE h.old_name = ? AND h.changed_at > ? AND u.is_deactivated = 0 ORDER BY h.id DESC LIMIT 1`, old, Ts(time.Now().Add(-UsernameHold))).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("username redirect %s: %w", old, err)
	}
	return cur, true, nil
}

// RenameRemoteUser follows a person on another server who changed their handle.
func (s *Store) RenameRemoteUser(id int64, newHandle string) error {
	if _, err := s.DB.Exec(`UPDATE users SET username = ? WHERE id = ? AND remote_host <> ''`, newHandle, id); err != nil {
		if isUnique(err, "users.username") {
			return ErrUsernameTaken
		}
		return fmt.Errorf("rename remote user %d: %w", id, err)
	}
	return nil
}
