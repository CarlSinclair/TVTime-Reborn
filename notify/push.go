package notify

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"tvt/logger"
	"tvt/secretbox"
	"tvt/store"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/hkdf"
)

// Web Push (RFC 8291 encryption, RFC 8292 VAPID), ported from CTime: delivered
// by the browser or OS even when no tab is open.

const (
	kvVAPIDPublic  = "vapid_public_key"
	kvVAPIDPrivate = "vapid_private_key_sealed"
	vapidLabel     = "vapid-private"
)

var ErrNoPushDevices = errors.New("no push devices registered")

type Pusher struct {
	Store   *store.Store
	Secret  string
	Subject string
	http    *http.Client
	mu      sync.Mutex
}

func NewPusher(st *store.Store, secret, subject string) *Pusher {
	return &Pusher{Store: st, Secret: secret, Subject: subject, http: &http.Client{Timeout: 10 * time.Second}}
}

func (p *Pusher) PublicKey() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pub := p.Store.KVGet(kvVAPIDPublic); pub != "" && p.Store.KVGet(kvVAPIDPrivate) != "" {
		return pub, nil
	}
	return p.generateLocked()
}

func (p *Pusher) generateLocked() (string, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate vapid key: %w", err)
	}
	pub := base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y))
	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return "", fmt.Errorf("marshal vapid key: %w", err)
	}
	sealed, err := secretbox.Seal(p.Secret, vapidLabel, base64.RawURLEncoding.EncodeToString(der))
	if err != nil {
		return "", err
	}
	if err := p.Store.KVSet(kvVAPIDPrivate, sealed); err != nil {
		return "", err
	}
	if err := p.Store.KVSet(kvVAPIDPublic, pub); err != nil {
		return "", err
	}
	logger.Info("PUSH generated new VAPID keypair")
	return pub, nil
}

func (p *Pusher) privateKey() (*ecdsa.PrivateKey, error) {
	if _, err := p.PublicKey(); err != nil {
		return nil, err
	}
	sealed := p.Store.KVGet(kvVAPIDPrivate)
	plain, err := secretbox.Open(p.Secret, vapidLabel, sealed)
	if err != nil {
		return nil, fmt.Errorf("open vapid private key: %w", err)
	}
	der, err := base64.RawURLEncoding.DecodeString(plain)
	if err != nil {
		return nil, fmt.Errorf("decode vapid private key: %w", err)
	}
	return x509.ParseECPrivateKey(der)
}

type pushPayload struct {
	Title       string `json:"title"`
	Body        string `json:"body"`
	Link        string `json:"link,omitempty"`
	UnreadCount int    `json:"unread_count"`
	Force       bool   `json:"force,omitempty"`
}

// SendToUser delivers to every device and returns how many accepted it. Dead
// subscriptions (404/410) are pruned as discovered.
func (p *Pusher) SendToUser(userID int64, title, body, link string, unread int) (int, error) {
	return p.sendToUser(userID, title, body, link, unread, false)
}

// SendTestToUser is for the Test button: the notification is always shown, even
// when the page is open in front, so the person can see it work.
func (p *Pusher) SendTestToUser(userID int64, title, body, link string, unread int) (int, error) {
	return p.sendToUser(userID, title, body, link, unread, true)
}

func (p *Pusher) sendToUser(userID int64, title, body, link string, unread int, force bool) (int, error) {
	subs, err := p.Store.PushSubs(userID)
	if err != nil {
		return 0, err
	}
	if len(subs) == 0 {
		return 0, ErrNoPushDevices
	}
	payload, err := json.Marshal(pushPayload{Title: title, Body: body, Link: link, UnreadCount: unread, Force: force})
	if err != nil {
		return 0, fmt.Errorf("marshal push payload uid=%d: %w", userID, err)
	}
	delivered := 0
	var lastErr error
	for _, sub := range subs {
		if err := p.send(sub, payload); err != nil {
			logger.Warn("PUSH send failed uid=%d endpoint_host=%s: %v", userID, hostOf(sub.Endpoint), err)
			lastErr = err
			continue
		}
		delivered++
	}
	if delivered == 0 && lastErr != nil {
		return 0, lastErr
	}
	return delivered, nil
}

func hostOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "?"
	}
	return u.Host
}

func (p *Pusher) send(sub store.PushSub, payload []byte) error {
	priv, err := p.privateKey()
	if err != nil {
		return fmt.Errorf("vapid key unavailable: %w", err)
	}
	body, headers, err := encrypt(sub, payload)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	ep, err := url.Parse(sub.Endpoint)
	if err != nil || ep.Scheme != "https" {
		return fmt.Errorf("invalid endpoint")
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"aud": ep.Scheme + "://" + ep.Host, "exp": time.Now().Add(12 * time.Hour).Unix(), "sub": p.Subject,
	})
	signed, err := tok.SignedString(priv)
	if err != nil {
		return fmt.Errorf("vapid jwt: %w", err)
	}
	pub := base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y))
	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", fmt.Sprintf("vapid t=%s, k=%s", signed, pub))
	req.Header.Set("TTL", "86400")
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		if derr := p.Store.DeletePushSub(sub.UserID, sub.Endpoint); derr != nil {
			logger.Error("PUSH prune dead subscription uid=%d: %v", sub.UserID, derr)
		}
		return fmt.Errorf("subscription expired (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("push service returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func b64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(s), "="))
}

// encrypt implements RFC 8291 aes128gcm for a single record.
func encrypt(sub store.PushSub, plaintext []byte) ([]byte, map[string]string, error) {
	clientPubRaw, err := b64(sub.P256dh)
	if err != nil {
		return nil, nil, fmt.Errorf("bad p256dh: %w", err)
	}
	authSecret, err := b64(sub.Auth)
	if err != nil {
		return nil, nil, fmt.Errorf("bad auth secret: %w", err)
	}
	curve := ecdh.P256()
	clientPub, err := curve.NewPublicKey(clientPubRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("bad client public key: %w", err)
	}
	serverPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serverPubRaw := serverPriv.PublicKey().Bytes()
	shared, err := serverPriv.ECDH(clientPub)
	if err != nil {
		return nil, nil, fmt.Errorf("ecdh: %w", err)
	}
	authInfo := append([]byte("WebPush: info\x00"), clientPubRaw...)
	authInfo = append(authInfo, serverPubRaw...)
	prkKey := hkdf.Extract(sha256.New, shared, authSecret)
	ikm := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.Expand(sha256.New, prkKey, authInfo), ikm); err != nil {
		return nil, nil, fmt.Errorf("hkdf ikm: %w", err)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	prk := hkdf.Extract(sha256.New, ikm, salt)
	cek := make([]byte, 16)
	if _, err := io.ReadFull(hkdf.Expand(sha256.New, prk, []byte("Content-Encoding: aes128gcm\x00")), cek); err != nil {
		return nil, nil, fmt.Errorf("hkdf cek: %w", err)
	}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(hkdf.Expand(sha256.New, prk, []byte("Content-Encoding: nonce\x00")), nonce); err != nil {
		return nil, nil, fmt.Errorf("hkdf nonce: %w", err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	padded := append(append([]byte{}, plaintext...), 0x02)
	ct := gcm.Seal(nil, nonce, padded, nil)
	header := make([]byte, 0, 16+4+1+len(serverPubRaw))
	header = append(header, salt...)
	rs := make([]byte, 4)
	binary.BigEndian.PutUint32(rs, 4096)
	header = append(header, rs...)
	header = append(header, byte(len(serverPubRaw)))
	header = append(header, serverPubRaw...)
	return append(header, ct...), map[string]string{"Content-Type": "application/octet-stream", "Content-Encoding": "aes128gcm"}, nil
}
