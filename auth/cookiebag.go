package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
)

// CookieSession is the only cookie the app sets. It carries every field the
// server needs to keep between requests, so a visitor's browser holds one
// cookie instead of one per purpose.
const CookieSession = "tt"

const (
	fieldAccess   = "a"
	fieldRefresh  = "r"
	fieldCSRF     = "c"
	fieldMFA      = "m"
	fieldWebAuthn = "w"
)

type bag map[string]string

func decodeBag(v string) bag {
	b := bag{}
	if v == "" {
		return b
	}
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return b
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return bag{}
	}
	return b
}

func (b bag) encode() string {
	raw, err := json.Marshal(b)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// BagField reads one field out of a raw session cookie value. It exists for
// tests and tools; the server reads fields through the Service.
func BagField(cookieValue, field string) string { return decodeBag(cookieValue)[field] }

func (s *Service) field(r *http.Request, name string) string {
	c, err := r.Cookie(CookieSession)
	if err != nil {
		return ""
	}
	return decodeBag(c.Value)[name]
}

// responseBag collects every change made to the session cookie while a request
// is handled and emits a single Set-Cookie header when the response starts.
type responseBag struct {
	http.ResponseWriter
	s     *Service
	bag   bag
	dirty bool
	sent  bool
}

// WrapResponse must wrap the writer of every request before handlers run.
func (s *Service) WrapResponse(w http.ResponseWriter, r *http.Request) http.ResponseWriter {
	b := bag{}
	if c, err := r.Cookie(CookieSession); err == nil {
		b = decodeBag(c.Value)
	}
	return &responseBag{ResponseWriter: w, s: s, bag: b}
}

func (rb *responseBag) flush() {
	if rb.sent {
		return
	}
	rb.sent = true
	if !rb.dirty {
		return
	}
	c := &http.Cookie{Name: CookieSession, Path: "/", HttpOnly: true, Secure: rb.s.Secure, SameSite: http.SameSiteLaxMode}
	if len(rb.bag) == 0 {
		c.MaxAge = -1
	} else {
		c.Value = rb.bag.encode()
		c.MaxAge = int(RefreshTokenDuration.Seconds())
	}
	http.SetCookie(rb.ResponseWriter, c)
}

func (rb *responseBag) WriteHeader(code int) {
	rb.flush()
	rb.ResponseWriter.WriteHeader(code)
}

func (rb *responseBag) Write(p []byte) (int, error) {
	rb.flush()
	return rb.ResponseWriter.Write(p)
}

func (rb *responseBag) Flush() {
	rb.flush()
	if f, ok := rb.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rb *responseBag) Unwrap() http.ResponseWriter { return rb.ResponseWriter }

func findBag(w http.ResponseWriter) *responseBag {
	for w != nil {
		if rb, ok := w.(*responseBag); ok {
			return rb
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = u.Unwrap()
	}
	return nil
}

// setField changes one field. An empty value removes it.
func (s *Service) setField(w http.ResponseWriter, name, value string) {
	rb := findBag(w)
	if rb == nil {
		b := bag{}
		if value != "" {
			b[name] = value
		}
		http.SetCookie(w, &http.Cookie{Name: CookieSession, Value: b.encode(), Path: "/", MaxAge: int(RefreshTokenDuration.Seconds()), HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode})
		return
	}
	if value == "" {
		delete(rb.bag, name)
	} else {
		rb.bag[name] = value
	}
	rb.dirty = true
}
