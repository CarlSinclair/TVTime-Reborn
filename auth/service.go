package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"tvt/logger"
	"tvt/store"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	AccessTokenDuration  = 15 * time.Minute
	RefreshTokenDuration = 30 * 24 * time.Hour
	RotationGrace        = 60 * time.Second
	EmailTokenDuration   = 30 * time.Minute

	PurposeLogin  = "login"
	PurposeReset  = "reset"
	PurposeVerify = "verify"
	PurposeEmail  = "emailchange"
)

var (
	ErrBadCredentials = errors.New("invalid credentials")
	ErrDeactivated    = errors.New("account deactivated")
	ErrBadToken       = errors.New("invalid or expired token")
	ErrPasswordRules  = errors.New("password does not meet requirements")
)

type Service struct {
	Store  *store.Store
	Secret []byte
	Secure bool
	pk     *passkeyState
}

func New(st *store.Store, secret string, secure bool) *Service {
	return &Service{Store: st, Secret: []byte(secret), Secure: secure}
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

const passwordSpecials = "!@#$%^&*()-_+=[]{}|;:'\",.<>/?`~"

// ValidPassword enforces the same rules as the other products: at least eight
// characters with an upper case letter, a lower case letter, a digit and a
// symbol. The upper limit is bcrypt's 72 bytes.
func ValidPassword(p string) error {
	if len(p) < 8 || len(p) > 72 {
		return ErrPasswordRules
	}
	var upper, lower, digit, special bool
	for _, c := range p {
		switch {
		case unicode.IsUpper(c):
			upper = true
		case unicode.IsLower(c):
			lower = true
		case unicode.IsDigit(c):
			digit = true
		case strings.ContainsRune(passwordSpecials, c):
			special = true
		}
	}
	if !upper || !lower || !digit || !special {
		return ErrPasswordRules
	}
	return nil
}

func HashPassword(p string) (string, error) {
	if err := ValidPassword(p); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(p), 12)
	if err != nil {
		return "", fmt.Errorf("bcrypt: %w", err)
	}
	return string(h), nil
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("tvt-dummy-password"), 12)

func (s *Service) Login(email, password string) (*store.User, error) {
	u, err := s.Store.UserByEmail(strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrBadCredentials
		}
		return nil, fmt.Errorf("login lookup: %w", err)
	}
	if u.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return nil, ErrBadCredentials
	}
	if u.IsDeactivated && !u.SelfDeactivated {
		return nil, ErrDeactivated
	}
	return u, nil
}

// CheckPassword verifies a password against an already loaded account.
func (s *Service) CheckPassword(u *store.User, password string) bool {
	if u.PasswordHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}

func (s *Service) signToken(userID int64, scope string, ttl time.Duration) (string, error) {
	return s.signSessionToken(userID, scope, ttl, "")
}

// signSessionToken adds the session id. Access tokens are only honoured while
// their session still has a row in refresh_tokens, so logging out, changing the
// password or revoking sessions ends them at once instead of at expiry.
func (s *Service) signSessionToken(userID int64, scope string, ttl time.Duration, sid string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"user_id": userID,
		"jti":     uuid.NewString(),
		"scope":   scope,
		"iat":     now.Unix(),
		"exp":     now.Add(ttl).Unix(),
	}
	if sid != "" {
		claims["sid"] = sid
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.Secret)
}

func (s *Service) parseToken(raw, scope string) (int64, error) {
	uid, _, err := s.parseSessionToken(raw, scope)
	return uid, err
}

func (s *Service) parseSessionToken(raw, scope string) (int64, string, error) {
	tok, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return s.Secret, nil
	})
	if err != nil || !tok.Valid {
		return 0, "", ErrBadToken
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return 0, "", ErrBadToken
	}
	if sc, _ := claims["scope"].(string); sc != scope {
		return 0, "", ErrBadToken
	}
	uid, ok := claims["user_id"].(float64)
	if !ok {
		return 0, "", ErrBadToken
	}
	sid, _ := claims["sid"].(string)
	return int64(uid), sid, nil
}

func (s *Service) ClearCookies(w http.ResponseWriter) {
	s.setField(w, fieldAccess, "")
	s.setField(w, fieldRefresh, "")
}

func (s *Service) storeRefresh(tx *sql.Tx, userID int64, sid, raw string, exp time.Time) error {
	_, err := tx.Exec(`INSERT INTO refresh_tokens (user_id, sid, token_hash, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, sid, hashToken(raw), store.Ts(exp), store.Now())
	return err
}

func (s *Service) sessionAlive(uid int64, sid string) bool {
	if sid == "" {
		return false
	}
	var n int
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE sid = ? AND user_id = ? AND expires_at > ?`, sid, uid, store.Now()).Scan(&n); err != nil {
		logger.Error("AUTH session lookup uid=%d: %v", uid, err)
		return false
	}
	return n > 0
}

func (s *Service) StartSession(w http.ResponseWriter, userID int64) error {
	if u, err := s.Store.UserByID(userID); err != nil {
		return fmt.Errorf("session user uid=%d: %w", userID, err)
	} else if u.IsDeactivated {
		if !u.SelfDeactivated {
			return ErrDeactivated
		}
		if err := s.Store.Reactivate(userID); err != nil {
			return fmt.Errorf("reactivate uid=%d: %w", userID, err)
		}
		logger.Info("AUTH account reactivated by signing in uid=%d", userID)
	}
	sid, err := randomToken()
	if err != nil {
		return fmt.Errorf("session id uid=%d: %w", userID, err)
	}
	refresh, err := s.signSessionToken(userID, "refresh", RefreshTokenDuration, sid)
	if err != nil {
		return fmt.Errorf("sign refresh uid=%d: %w", userID, err)
	}
	access, err := s.signSessionToken(userID, "access", AccessTokenDuration, sid)
	if err != nil {
		return fmt.Errorf("sign access uid=%d: %w", userID, err)
	}
	tx, err := s.Store.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin session uid=%d: %w", userID, err)
	}
	defer tx.Rollback()
	if err := s.storeRefresh(tx, userID, sid, refresh, time.Now().Add(RefreshTokenDuration)); err != nil {
		return fmt.Errorf("store refresh uid=%d: %w", userID, err)
	}
	if _, err := tx.Exec(`DELETE FROM refresh_tokens WHERE user_id = ? AND (expires_at < ? OR (superseded_at IS NOT NULL AND superseded_at < ?))`,
		userID, store.Now(), store.Ts(time.Now().Add(-time.Hour))); err != nil {
		return fmt.Errorf("prune refresh uid=%d: %w", userID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session uid=%d: %w", userID, err)
	}
	s.setField(w, fieldAccess, access)
	s.setField(w, fieldRefresh, refresh)
	return nil
}

// Authenticate resolves the signed-in user for a request. An expired access
// token is renewed from the refresh cookie. Rotation is claimed with a single
// conditional UPDATE so that when several concurrent requests present the same
// refresh token, exactly one rotates it; the others are inside the grace window
// and only receive a fresh access token (the winner's Set-Cookie carries the
// new refresh token). A superseded token outside the window is rejected.
func (s *Service) Authenticate(w http.ResponseWriter, r *http.Request) (*store.User, error) {
	if tok := s.field(r, fieldAccess); tok != "" {
		if uid, sid, err := s.parseSessionToken(tok, "access"); err == nil && s.sessionAlive(uid, sid) {
			return s.loadUser(uid)
		}
	}
	refreshTok := s.field(r, fieldRefresh)
	if refreshTok == "" {
		return nil, ErrBadToken
	}
	uid, sid, err := s.parseSessionToken(refreshTok, "refresh")
	if err != nil {
		s.ClearCookies(w)
		return nil, ErrBadToken
	}
	oldHash := hashToken(refreshTok)

	var supersededAt sql.NullString
	var expires string
	err = s.Store.DB.QueryRow(`SELECT expires_at, superseded_at FROM refresh_tokens WHERE token_hash = ? AND user_id = ?`, oldHash, uid).Scan(&expires, &supersededAt)
	if err != nil {
		s.ClearCookies(w)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrBadToken
		}
		return nil, fmt.Errorf("lookup refresh uid=%d: %w", uid, err)
	}
	if store.ParseTs(expires).Before(time.Now()) {
		s.ClearCookies(w)
		return nil, ErrBadToken
	}
	u, err := s.loadUser(uid)
	if err != nil {
		s.ClearCookies(w)
		return nil, err
	}

	newRefresh, err := s.signSessionToken(uid, "refresh", RefreshTokenDuration, sid)
	if err != nil {
		return nil, fmt.Errorf("sign refresh uid=%d: %w", uid, err)
	}
	newHash := hashToken(newRefresh)
	tx, err := s.Store.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin rotate uid=%d: %w", uid, err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE refresh_tokens SET superseded_at = ?, superseded_by = ? WHERE token_hash = ? AND superseded_at IS NULL`,
		store.Now(), newHash, oldHash)
	if err != nil {
		return nil, fmt.Errorf("claim rotation uid=%d: %w", uid, err)
	}
	won, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("rotation rows uid=%d: %w", uid, err)
	}
	if won == 1 {
		if err := s.storeRefresh(tx, uid, sid, newRefresh, time.Now().Add(RefreshTokenDuration)); err != nil {
			return nil, fmt.Errorf("store rotated refresh uid=%d: %w", uid, err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit rotate uid=%d: %w", uid, err)
		}
		s.setField(w, fieldRefresh, newRefresh)
	} else {
		tx.Rollback()
		var sup string
		if err := s.Store.DB.QueryRow(`SELECT superseded_at FROM refresh_tokens WHERE token_hash = ?`, oldHash).Scan(&sup); err != nil {
			return nil, fmt.Errorf("read superseded uid=%d: %w", uid, err)
		}
		if time.Since(store.ParseTs(sup)) > RotationGrace {
			logger.Warn("AUTH superseded refresh token reused outside grace uid=%d, revoking all sessions", uid)
			if _, err := s.Store.DB.Exec(`DELETE FROM refresh_tokens WHERE user_id = ?`, uid); err != nil {
				logger.Error("AUTH revoke sessions after reuse uid=%d: %v", uid, err)
			}
			s.ClearCookies(w)
			return nil, ErrBadToken
		}
	}
	access, err := s.signSessionToken(uid, "access", AccessTokenDuration, sid)
	if err != nil {
		return nil, fmt.Errorf("sign access uid=%d: %w", uid, err)
	}
	s.setField(w, fieldAccess, access)
	return u, nil
}

func (s *Service) loadUser(uid int64) (*store.User, error) {
	u, err := s.Store.UserByID(uid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrBadToken
		}
		return nil, fmt.Errorf("load user uid=%d: %w", uid, err)
	}
	if u.IsDeactivated {
		return nil, ErrDeactivated
	}
	return u, nil
}

func (s *Service) EndSession(w http.ResponseWriter, r *http.Request) error {
	var firstErr error
	if tok := s.field(r, fieldRefresh); tok != "" {
		_, sid, _ := s.parseSessionToken(tok, "refresh")
		if _, err := s.Store.DB.Exec(`DELETE FROM refresh_tokens WHERE token_hash = ? OR (sid <> '' AND sid = ?)`, hashToken(tok), sid); err != nil {
			firstErr = fmt.Errorf("delete session on logout: %w", err)
		}
	}
	s.ClearCookies(w)
	return firstErr
}

func (s *Service) RevokeAll(userID int64) error {
	if _, err := s.Store.DB.Exec(`DELETE FROM refresh_tokens WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("revoke all uid=%d: %w", userID, err)
	}
	return nil
}

func (s *Service) IssueEmailToken(userID int64, purpose string) (string, error) {
	raw, err := randomToken()
	if err != nil {
		return "", err
	}
	if _, err := s.Store.DB.Exec(`INSERT INTO email_tokens (token_hash, user_id, purpose, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		hashToken(raw), userID, purpose, store.Ts(time.Now().Add(EmailTokenDuration)), store.Now()); err != nil {
		return "", fmt.Errorf("store email token uid=%d purpose=%s: %w", userID, purpose, err)
	}
	return raw, nil
}

// ConsumeEmailToken atomically marks a token used; two concurrent submissions
// of the same link cannot both succeed.
func (s *Service) ConsumeEmailToken(raw, purpose string) (int64, error) {
	h := hashToken(raw)
	res, err := s.Store.DB.Exec(`UPDATE email_tokens SET used_at = ? WHERE token_hash = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?`,
		store.Now(), h, purpose, store.Now())
	if err != nil {
		return 0, fmt.Errorf("consume email token purpose=%s: %w", purpose, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("consume email token rows: %w", err)
	}
	if n != 1 {
		return 0, ErrBadToken
	}
	var uid int64
	if err := s.Store.DB.QueryRow(`SELECT user_id FROM email_tokens WHERE token_hash = ?`, h).Scan(&uid); err != nil {
		return 0, fmt.Errorf("read email token owner: %w", err)
	}
	return uid, nil
}
