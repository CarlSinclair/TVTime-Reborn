package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"net/http"
	"strings"
	"time"

	"tvt/secretbox"
	"tvt/store"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	qrcode "github.com/skip2/go-qrcode"
)

const (
	MFATokenDuration  = 5 * time.Minute
	recoveryCodeCount = 10
	totpLabel         = "totp-secret"
)

var ErrBadMFACode = errors.New("invalid verification code")

func (s *Service) sealSecret(plain string) (string, error) {
	return secretbox.Seal(string(s.Secret), totpLabel, plain)
}

func (s *Service) openSecret(sealed string) (string, error) {
	return secretbox.Open(string(s.Secret), totpLabel, sealed)
}

// BeginTOTPSetup stores a fresh, not-yet-enabled secret and returns the
// otpauth URI and a PNG QR for it.
func (s *Service) BeginTOTPSetup(u *store.User, issuer string) (secret, uri string, qrPNG []byte, err error) {
	account := u.Email
	if account == "" {
		account = u.Username
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: account})
	if err != nil {
		return "", "", nil, fmt.Errorf("generate totp key uid=%d: %w", u.ID, err)
	}
	sealed, err := s.sealSecret(key.Secret())
	if err != nil {
		return "", "", nil, err
	}
	if err := s.Store.SetTOTPSecret(u.ID, sealed); err != nil {
		return "", "", nil, err
	}
	var buf bytes.Buffer
	img, err := qrcode.New(key.URL(), qrcode.Medium)
	if err != nil {
		return "", "", nil, fmt.Errorf("render totp qr uid=%d: %w", u.ID, err)
	}
	if err := png.Encode(&buf, img.Image(256)); err != nil {
		return "", "", nil, fmt.Errorf("encode totp qr uid=%d: %w", u.ID, err)
	}
	return key.Secret(), key.URL(), buf.Bytes(), nil
}

// checkTOTP validates a code within one step of now and returns the matched
// time step so the caller can claim it against replay.
func (s *Service) checkTOTP(u *store.User, code string) (int64, error) {
	if u.TOTPSecretEnc == "" {
		return 0, ErrBadMFACode
	}
	secret, err := s.openSecret(u.TOTPSecretEnc)
	if err != nil {
		return 0, fmt.Errorf("open totp secret uid=%d: %w", u.ID, err)
	}
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	now := time.Now()
	for _, off := range []int{0, -1, 1} {
		t := now.Add(time.Duration(off) * 30 * time.Second)
		ok, err := totp.ValidateCustom(code, secret, t, totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err != nil {
			continue
		}
		if ok {
			return t.Unix() / 30, nil
		}
	}
	return 0, ErrBadMFACode
}

// EnableTOTP confirms the first code from the authenticator app, turns 2FA on
// and returns the one-time recovery codes in plaintext.
func (s *Service) EnableTOTP(u *store.User, code string) ([]string, error) {
	step, err := s.checkTOTP(u, code)
	if err != nil {
		return nil, err
	}
	plain, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := s.Store.EnableTOTP(u.ID, step, hashes); err != nil {
		return nil, err
	}
	return plain, nil
}

func (s *Service) RegenerateRecoveryCodes(u *store.User) ([]string, error) {
	plain, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	step := u.TOTPLastStep
	if err := s.Store.EnableTOTP(u.ID, step, hashes); err != nil {
		return nil, err
	}
	return plain, nil
}

func newRecoveryCodes() (plain, hashes []string, err error) {
	for i := 0; i < recoveryCodeCount; i++ {
		b := make([]byte, 5)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, fmt.Errorf("recovery code entropy: %w", err)
		}
		h := strings.ToUpper(hex.EncodeToString(b))
		code := h[:5] + "-" + h[5:]
		plain = append(plain, code)
		hashes = append(hashes, hashRecovery(code))
	}
	return plain, hashes, nil
}

func hashRecovery(code string) string {
	norm := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), " ", ""))
	h := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(h[:])
}

// VerifySecondFactor accepts either a current TOTP code or an unused recovery
// code. A TOTP step can be used once; a recovery code is burned on use.
func (s *Service) VerifySecondFactor(u *store.User, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return ErrBadMFACode
	}
	if strings.Contains(code, "-") || len(strings.ReplaceAll(code, " ", "")) > 6 {
		ok, err := s.Store.ConsumeRecoveryCode(u.ID, hashRecovery(code))
		if err != nil {
			return err
		}
		if !ok {
			return ErrBadMFACode
		}
		return nil
	}
	step, err := s.checkTOTP(u, code)
	if err != nil {
		return err
	}
	claimed, err := s.Store.ClaimTOTPStep(u.ID, step)
	if err != nil {
		return err
	}
	if !claimed {
		return ErrBadMFACode
	}
	return nil
}

func (s *Service) DisableTOTP(userID int64) error { return s.Store.DisableTOTP(userID) }

// BeginMFA parks a login that passed its first factor: the user holds a
// short-lived signed cookie that is only good for the second-factor endpoint.
func (s *Service) BeginMFA(w http.ResponseWriter, userID int64) error {
	tok, err := s.signToken(userID, "mfa", MFATokenDuration)
	if err != nil {
		return fmt.Errorf("sign mfa token uid=%d: %w", userID, err)
	}
	s.setField(w, fieldMFA, tok)
	return nil
}

func (s *Service) PendingMFAUser(r *http.Request) (int64, error) {
	tok := s.field(r, fieldMFA)
	if tok == "" {
		return 0, ErrBadToken
	}
	return s.parseToken(tok, "mfa")
}

func (s *Service) ClearMFA(w http.ResponseWriter) { s.setField(w, fieldMFA, "") }
