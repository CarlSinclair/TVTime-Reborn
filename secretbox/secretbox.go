package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	shared "github.com/CarlSinclair/csuit-libs/secretbox"
)

func ring(secret string) (*shared.Keyring, error) {
	master := sha256.Sum256([]byte("tvt-secretbox-master:" + secret))
	sum := sha256.Sum256(master[:])
	id := hex.EncodeToString(sum[:4])
	return shared.New(id, map[string][]byte{id: master[:]})
}

func Seal(secret, label, plaintext string) (string, error) {
	r, err := ring(secret)
	if err != nil {
		return "", err
	}
	return r.Seal(label, plaintext)
}

func Open(secret, label, sealed string) (string, error) {
	if strings.HasPrefix(sealed, shared.PrefixV2) {
		r, err := ring(secret)
		if err != nil {
			return "", err
		}
		return r.Open(label, sealed)
	}
	return openLegacy(secret, label, sealed)
}

func NeedsMigration(sealed string) bool {
	return sealed != "" && !strings.HasPrefix(sealed, shared.PrefixV2)
}

func Reseal(secret, label, sealed string) (string, bool, error) {
	if !NeedsMigration(sealed) {
		return sealed, false, nil
	}
	plain, err := Open(secret, label, sealed)
	if err != nil {
		return "", false, err
	}
	out, err := Seal(secret, label, plain)
	if err != nil {
		return "", false, err
	}
	return out, true, nil
}

func legacyKey(secret, label string) []byte {
	h := sha256.Sum256([]byte(label + ":" + secret))
	return h[:]
}

func openLegacy(secret, label, sealed string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("secretbox decode: %w", err)
	}
	block, err := aes.NewCipher(legacyKey(secret, label))
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
