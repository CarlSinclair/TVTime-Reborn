package federation

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"tvt/db"
	"tvt/store"
)

const recvHost = "recv.example.test"

type harness struct {
	t    testing.TB
	st   *store.Store
	svc  *Service
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	from string
}

func newHarness(t testing.TB) *harness {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(d)
	svc, err := New(st, nil, recvHost, "0123456789abcdef0123456789abcdef", true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := &harness{t: t, st: st, svc: svc, pub: pub, priv: priv, from: "send.example.test"}
	if err := st.PinPeer(h.from, base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) user(name string) *store.User {
	u, err := h.st.CreateUser(name+"@x.io", name, "", "en", false)
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}

func (h *harness) send(priv ed25519.PrivateKey, from string, body []byte) *httptest.ResponseRecorder {
	date := strconv.FormatInt(time.Now().Unix(), 10)
	digest := digestOf(body)
	sig := ed25519.Sign(priv, canonical("POST", "/federation/inbox", recvHost, date, digest))
	req := httptest.NewRequest("POST", "/federation/inbox", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.5:4000"
	req.Header.Set("X-TVT-Host", from)
	req.Header.Set("X-TVT-Date", date)
	req.Header.Set("X-TVT-Digest", digest)
	req.Header.Set("X-TVT-Signature", base64.StdEncoding.EncodeToString(sig))
	rec := httptest.NewRecorder()
	h.svc.Inbox(rec, req)
	return rec
}

func (h *harness) msg(typ, fromUser, to string) []byte {
	b, _ := json.Marshal(message{Type: typ, ID: newID(), From: Person{Username: fromUser, Name: fromUser}, To: to})
	return b
}

func TestReplayIsRejectedEvenAfterRestart(t *testing.T) {
	h := newHarness(t)
	h.user("alice")
	body := h.msg("friend_request", "bobby", "alice")
	date := strconv.FormatInt(time.Now().Unix(), 10)
	digest := digestOf(body)
	sig := ed25519.Sign(h.priv, canonical("POST", "/federation/inbox", recvHost, date, digest))
	call := func(svc *Service) int {
		req := httptest.NewRequest("POST", "/federation/inbox", bytes.NewReader(body))
		req.RemoteAddr = "203.0.113.5:4000"
		req.Header.Set("X-TVT-Host", h.from)
		req.Header.Set("X-TVT-Date", date)
		req.Header.Set("X-TVT-Digest", digest)
		req.Header.Set("X-TVT-Signature", base64.StdEncoding.EncodeToString(sig))
		rec := httptest.NewRecorder()
		svc.Inbox(rec, req)
		return rec.Code
	}
	if code := call(h.svc); code != http.StatusNoContent {
		t.Fatalf("first delivery: %d", code)
	}
	if code := call(h.svc); code != http.StatusConflict {
		t.Fatalf("replay: %d", code)
	}
	restarted, err := New(h.st, nil, recvHost, "0123456789abcdef0123456789abcdef", true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code := call(restarted); code != http.StatusConflict {
		t.Fatalf("a replay after a restart must still be refused: %d", code)
	}
}

func TestOnlyOnePendingRequestPerPairButOthersAreNotBlocked(t *testing.T) {
	h := newHarness(t)
	alice := h.user("alice")
	for i := 0; i < 3; i++ {
		if rec := h.send(h.priv, h.from, h.msg("friend_request", "bobby", "alice")); rec.Code != http.StatusNoContent {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	if n, _ := h.st.PendingIncoming(alice.ID, h.from); n != 1 {
		t.Fatalf("repeating a request while it waits must not add another: %d pending", n)
	}
	for i := 0; i < 12; i++ {
		name := "user" + strconv.Itoa(100+i)
		h.send(h.priv, h.from, h.msg("friend_request", name, "alice"))
	}
	if n, _ := h.st.PendingIncoming(alice.ID, h.from); n != 13 {
		t.Fatalf("other people must always be able to ask: %d pending, want 13", n)
	}
}

func TestChangedKeyIsReportedNotSilentlyFailed(t *testing.T) {
	h := newHarness(t)
	h.user("alice")
	otherPub, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.TrimPrefix(r.Host, "")
		json.NewEncoder(w).Encode(wellKnown{Version: 1, Host: host, Key: base64.StdEncoding.EncodeToString(otherPub)})
	}))
	defer ts.Close()
	host := strings.TrimPrefix(ts.URL, "http://")
	if err := h.st.PinPeer(host, base64.StdEncoding.EncodeToString(h.pub)); err != nil {
		t.Fatal(err)
	}
	rec := h.send(otherPriv, host, h.msg("friend_request", "bobby", "alice"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "key changed") {
		t.Fatalf("a rotated key must be refused with a clear reason: %d %q", rec.Code, rec.Body.String())
	}
}

func TestBadSignatureAndClaimsDoNotChargeAnotherServer(t *testing.T) {
	h := newHarness(t)
	h.user("alice")
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)
	for i := 0; i < 300; i++ {
		h.svc.hostLimit.Allow("noise")
	}
	for i := 0; i < 60; i++ {
		if rec := h.send(wrong, h.from, h.msg("friend_request", "bobby", "alice")); rec.Code == http.StatusNoContent {
			t.Fatalf("forged message accepted")
		}
	}
	if rec := h.send(h.priv, h.from, h.msg("friend_request", "carol", "alice")); rec.Code != http.StatusNoContent {
		t.Fatalf("forgeries claiming a server must not use up that server's allowance: %d", rec.Code)
	}
}

func FuzzInbox(f *testing.F) {
	f.Add([]byte(`{"type":"friend_request","from":{"username":"bobby"},"to":"alice"}`))
	f.Add([]byte(`{"type":"activity","from":{"username":"bobby"},"event":{"ref":"w:1","kind":"watch","show_tmdb":9999999999999,"season":-5,"episode":99999999999,"count":-1,"stars":99,"at":"junk","poster":"../../etc/passwd"}}`))
	f.Add([]byte(`{"type":"retract","from":{"username":"bobby"},"ref":"` + strings.Repeat("x", 500) + `"}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"type":123}`))
	f.Add([]byte(`{"from":{"username":"‮bobby"},"type":"friend_end","to":"alice"}`))
	h := newHarness(f)
	h.user("alice")
	f.Fuzz(func(t *testing.T, body []byte) {
		rec := h.send(h.priv, h.from, body)
		if rec.Code >= 500 {
			t.Fatalf("garbage must never crash the inbox: %d %q", rec.Code, rec.Body.String())
		}
	})
}
