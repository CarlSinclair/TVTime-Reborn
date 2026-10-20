package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"tvt/logger"
	"tvt/store"
)

type ctxKey int

const userKey ctxKey = 1

func WithUser(ctx context.Context, u *store.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

func UserFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userKey).(*store.User)
	return u
}

func (s *Service) CSRFToken(w http.ResponseWriter, r *http.Request) string {
	if v := s.field(r, fieldCSRF); len(v) == 64 {
		return v
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		logger.Error("AUTH csrf token generation failed: %v", err)
		return ""
	}
	tok := hex.EncodeToString(b)
	s.setField(w, fieldCSRF, tok)
	return tok
}

func (s *Service) CSRFValid(r *http.Request) bool {
	cookie := s.field(r, fieldCSRF)
	if len(cookie) != 64 {
		return false
	}
	sent := r.Header.Get("X-CSRF-Token")
	if sent == "" {
		sent = r.PostFormValue("csrf")
	}
	return subtle.ConstantTimeCompare([]byte(sent), []byte(cookie)) == 1
}

// ClientIPHeader names the header a trusted reverse proxy uses to pass the
// caller's address. For the default, X-Forwarded-For, the right-most entry is
// used: it is the one the proxy itself appended, so anything a client put on
// the left is ignored. A header such as CF-Connecting-IP carries a single
// address.
var ClientIPHeader = "X-Forwarded-For"

// ClientIP returns the caller's address. The forwarding header is honoured only
// when the direct peer is a loopback or private address (the local reverse
// proxy); from anywhere else it is attacker-controlled and ignored.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer != nil && (peer.IsLoopback() || peer.IsPrivate()) {
		for _, name := range []string{ClientIPHeader, "X-Forwarded-For"} {
			v := r.Header.Get(name)
			if v == "" {
				continue
			}
			parts := strings.Split(v, ",")
			if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
				logger.RememberCountry(ip.String(), headerCountry(r))
				return ip.String()
			}
		}
	}
	return host
}

// ClientCountry returns the two-letter country code a trusted proxy (for
// example Cloudflare's CF-IPCountry) attached to the request, or "??". It is
// what logs record instead of the address.
func ClientCountry(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || !(peer.IsLoopback() || peer.IsPrivate()) {
		return "??"
	}
	if c := headerCountry(r); c != "" {
		return c
	}
	return "??"
}

func headerCountry(r *http.Request) string {
	c := strings.ToUpper(strings.TrimSpace(r.Header.Get("CF-IPCountry")))
	if len(c) != 2 || c[0] < 'A' || c[0] > 'Z' || c[1] < 'A' || c[1] > 'Z' {
		return ""
	}
	return c
}

type Limiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	window time.Duration
	max    int
}

func NewLimiter(max int, window time.Duration) *Limiter {
	l := &Limiter{hits: map[string][]time.Time{}, window: window, max: max}
	go func() {
		for range time.Tick(5 * time.Minute) {
			l.mu.Lock()
			cut := time.Now().Add(-l.window)
			for k, v := range l.hits {
				if len(v) == 0 || v[len(v)-1].Before(cut) {
					delete(l.hits, k)
				}
			}
			l.mu.Unlock()
		}
	}()
	return l
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cut := now.Add(-l.window)
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}
