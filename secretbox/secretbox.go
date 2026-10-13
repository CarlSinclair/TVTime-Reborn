package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

func key(secret, label string) []byte {
	h := sha256.Sum256([]byte(label + ":" + secret))
	return h[:]
}

// Seal encrypts plaintext with AES-256-GCM under a key derived from the app
// secret and a purpose label, so a value sealed for one purpose cannot be
// opened as another.
func Seal(secret, label, plaintext string) (string, error) {
	block, err := aes.NewCipher(key(secret, label))
	if err != nil {
		return "", fmt.Errorf("secretbox cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("secretbox gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secretbox nonce: %w", err)
	}
	out := gcm.Seal(nonce, nonce, []byte(plaintext), []byte(label))
	return base64.RawURLEncoding.EncodeToString(out), nil
}

func Open(secret, label, sealed string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("secretbox decode: %w", err)
	}
	block, err := aes.NewCipher(key(secret, label))
	if err != nil {
		return "", fmt.Errorf("secretbox cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("secretbox gcm: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("secretbox: ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(label))
	if err != nil {
		return "", fmt.Errorf("secretbox open: %w", err)
	}
	return string(plain), nil
}
