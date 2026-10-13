package federation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"tvt/auth"
	"tvt/logger"
	"tvt/notify"
	"tvt/secretbox"
	"tvt/store"
)

const (
	keyName   = "fed_ed25519_private"
	keyLabel  = "federation-key"
	dateSkew  = 5 * time.Minute
	inboxPath = "/federation/inbox"
)

var (
	ErrKeyChanged = errors.New("federation: server key changed")
	ErrNotFound   = errors.New("federation: person not found")
	nameRe        = regexp.MustCompile(`^[A-Za-z0-9_]{3,20}$`)
)

type Service struct {
	Store    *store.Store
	Notify   *notify.Service
	Host     string
	Secret   string
	Insecure bool
	Block    map[string]bool
	Allow    map[string]bool

	priv      ed25519.PrivateKey
	pub       ed25519.PublicKey
	client    *http.Client
	ipLimit   *auth.Limiter
	hostLimit *auth.Limiter
	ipFetch   *auth.Limiter
	allFetch  *auth.Limiter
	userLim   *auth.Limiter
}

// New prepares the server's identity: an Ed25519 key created on first start
// and kept sealed in the database.
func New(st *store.Store, nt *notify.Service, host, secret string, insecure bool, block, allow []string) (*Service, error) {
	s := &Service{Store: st, Notify: nt, Host: strings.ToLower(host), Secret: secret, Insecure: insecure, Block: map[string]bool{}, Allow: map[string]bool{},
		client: newClient(insecure), ipLimit: auth.NewLimiter(120, time.Minute), hostLimit: auth.NewLimiter(240, time.Minute),
		ipFetch: auth.NewLimiter(10, time.Minute), allFetch: auth.NewLimiter(60, time.Minute), userLim: auth.NewLimiter(120, time.Minute)}
	for _, b := range block {
		if b = strings.ToLower(strings.TrimSpace(b)); b != "" {
			s.Block[b] = true
		}
	}
	for _, a := range allow {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			s.Allow[a] = true
		}
	}
	if err := s.loadKey(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) loadKey() error {
	if sealed := s.Store.KVGet(keyName); sealed != "" {
		plain, err := secretbox.Open(s.Secret, keyLabel, sealed)
		if err != nil {
			return fmt.Errorf("open federation key: %w", err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(plain)
		if err != nil || len(raw) != ed25519.PrivateKeySize {
			return errors.New("federation key is corrupt")
		}
		s.priv = ed25519.PrivateKey(raw)
		s.pub = s.priv.Public().(ed25519.PublicKey)
		return nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate federation key: %w", err)
	}
	sealed, err := secretbox.Seal(s.Secret, keyLabel, base64.RawURLEncoding.EncodeToString(priv))
	if err != nil {
		return fmt.Errorf("seal federation key: %w", err)
	}
	if err := s.Store.KVSet(keyName, sealed); err != nil {
		return err
	}
	s.priv, s.pub = priv, pub
	logger.Info("FEDERATION generated this server's key")
	return nil
}

func (s *Service) scheme() string {
	if s.Insecure {
		return "http"
	}
	return "https"
}

func canonical(method, path, host, date, digest string) []byte {
	return []byte("TVT1\n" + method + "\n" + path + "\n" + strings.ToLower(host) + "\n" + date + "\n" + digest)
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return base64.StdEncoding.EncodeToString(sum[:])
}

type wellKnown struct {
	Version int    `json:"version"`
	Host    string `json:"host"`
	Key     string `json:"key"`
}

func (s *Service) WellKnown(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	json.NewEncoder(w).Encode(wellKnown{Version: 1, Host: s.Host, Key: base64.StdEncoding.EncodeToString(s.pub)})
}

type userInfo struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

func (s *Service) UserInfo(w http.ResponseWriter, r *http.Request) {
	if !s.userLim.Allow("u:" + auth.ClientIP(r)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	u, err := s.Store.UserByUsername(name)
	if err != nil || u.IsDeactivated || u.IsRemote() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(userInfo{Username: u.Username, Name: u.Name()})
}

func (s *Service) fetch(ctx context.Context, host, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.scheme()+"://"+host+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	body, err := readLimited(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("federation: bad JSON from %s: %w", host, err)
	}
	return resp.StatusCode, nil
}

// peerKey returns the pinned key of another server, learning it from its
// well-known document on first contact. A later change is refused until a
// human clears the pin.
func (s *Service) peerKey(ctx context.Context, host string) (ed25519.PublicKey, error) {
	host = strings.ToLower(host)
	if !ValidHost(host, s.Insecure) || s.Block[host] || host == s.Host || (len(s.Allow) > 0 && !s.Allow[host]) {
		return nil, ErrBadHost
	}
	if p, err := s.Store.PeerByHost(host); err == nil {
		if p.Blocked {
			return nil, ErrBadHost
		}
		raw, err := base64.StdEncoding.DecodeString(p.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, errors.New("federation: stored key is corrupt")
		}
		return ed25519.PublicKey(raw), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if !s.allFetch.Allow("all") {
		return nil, fmt.Errorf("%w: too many new servers at once", ErrUnreachable)
	}
	doc, err := s.currentDoc(ctx, host)
	if err != nil {
		return nil, err
	}
	if err := s.Store.PinPeer(host, doc.Key); err != nil {
		return nil, err
	}
	logger.Info("FEDERATION pinned the key of %s", host)
	raw, _ := base64.StdEncoding.DecodeString(doc.Key)
	return ed25519.PublicKey(raw), nil
}

// NormalizeHost reduces what an admin typed (possibly a full URL) to a bare
// lower-case host[:port].
func NormalizeHost(in string) string {
	h := strings.ToLower(strings.TrimSpace(in))
	for _, p := range []string{"https://", "http://"} {
		h = strings.TrimPrefix(h, p)
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	return h
}

// Connect introduces this server to another one at an admin's request: the
// other server's key is fetched from its well-known document and pinned.
func (s *Service) Connect(ctx context.Context, host string) (string, error) {
	host = NormalizeHost(host)
	if _, err := s.peerKey(ctx, host); err != nil {
		return host, err
	}
	return host, nil
}

func (s *Service) currentDoc(ctx context.Context, host string) (*wellKnown, error) {
	var doc wellKnown
	code, err := s.fetch(ctx, host, "/.well-known/tvt", &doc)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK || doc.Version != 1 || !strings.EqualFold(doc.Host, host) {
		return nil, fmt.Errorf("%w: %s does not run TVT federation", ErrUnreachable, host)
	}
	raw, err := base64.StdEncoding.DecodeString(doc.Key)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("federation: %s published an invalid key", host)
	}
	return &doc, nil
}

// keyChanged compares what a server publishes now with the key pinned for it.
// It is only asked after a signature failed, so a rotated key shows up as a
// clear warning for the operator rather than as silent bad signatures.
func (s *Service) keyChanged(ctx context.Context, host string) bool {
	if !s.allFetch.Allow("all") {
		return false
	}
	p, err := s.Store.PeerByHost(host)
	if err != nil {
		return false
	}
	doc, err := s.currentDoc(ctx, host)
	if err != nil || doc.Key == p.PublicKey {
		return false
	}
	logger.Warn("FEDERATION the key of %s changed; messages are refused until an admin forgets it under Admin > Servers", host)
	return true
}

// ResolveUser finds a person by name on another server and makes sure there is
// a local stand-in for them.
func (s *Service) ResolveUser(ctx context.Context, name, host string) (*store.User, error) {
	host = strings.ToLower(host)
	if !nameRe.MatchString(name) {
		return nil, ErrNotFound
	}
	if _, err := s.peerKey(ctx, host); err != nil {
		return nil, err
	}
	var info userInfo
	code, err := s.fetch(ctx, host, "/federation/user/"+url.PathEscape(name), &info)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if code != http.StatusOK || !strings.EqualFold(info.Username, name) {
		return nil, fmt.Errorf("%w: %s answered %d", ErrUnreachable, host, code)
	}
	return s.Store.EnsureRemoteUser(host, info.Username, cleanName(info.Name))
}

// stripFormat removes invisible formatting characters (zero-width joiners,
// bidirectional overrides and the like) that can make text look like something
// else.
func stripFormat(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, s)
}

func cleanName(n string) string {
	n = strings.TrimSpace(stripFormat(n))
	for _, c := range n {
		if c < 32 {
			return ""
		}
	}
	if r := []rune(n); len(r) > 40 {
		n = string(r[:40])
	}
	return n
}

// ---- outbound ----

type Person struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

type Event struct {
	Ref      string `json:"ref"`
	Kind     string `json:"kind"`
	ShowTMDB int64  `json:"show_tmdb,omitempty"`
	Show     string `json:"show,omitempty"`
	Poster   string `json:"poster,omitempty"`
	Season   int    `json:"season,omitempty"`
	Episode  int    `json:"episode,omitempty"`
	Count    int    `json:"count,omitempty"`
	Stars    int    `json:"stars,omitempty"`
	Excerpt  string `json:"excerpt,omitempty"`
	At       string `json:"at"`
}

type message struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	From  Person `json:"from"`
	To    string `json:"to,omitempty"`
	Event *Event `json:"event,omitempty"`
	Ref   string `json:"ref,omitempty"`
	// NewName is the username a person changed to; From carries the old one.
	NewName string `json:"new_name,omitempty"`
	// Comment is an episode comment shared with every allow-listed server.
	Comment *CommentMsg `json:"comment,omitempty"`
	Reason  string      `json:"reason,omitempty"`
}

// CommentMsg is an episode comment as it travels between servers. Ref names it
// on its home server; Parent names the comment it answers, on the same server.
type CommentMsg struct {
	Ref      string `json:"ref"`
	Parent   string `json:"parent,omitempty"`
	ShowTMDB int64  `json:"show_tmdb"`
	Season   int    `json:"season"`
	Episode  int    `json:"episode"`
	Body     string `json:"body"`
	Spoiler  bool   `json:"spoiler,omitempty"`
	Created  string `json:"created"`
	Edited   string `json:"edited,omitempty"`
}

func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Service) enqueue(host string, m message) {
	m.ID = newID()
	body, err := json.Marshal(m)
	if err != nil {
		logger.Error("FEDERATION encode %s: %v", m.Type, err)
		return
	}
	if err := s.Store.Enqueue(host, inboxPath, string(body)); err != nil {
		logger.Error("FEDERATION %v", err)
	}
}

func person(u *store.User) Person { return Person{Username: u.LocalPart(), Name: u.Name()} }

func (s *Service) SendFriendRequest(from, to *store.User) {
	s.enqueue(to.RemoteHost, message{Type: "friend_request", From: person(from), To: to.LocalPart()})
}

func (s *Service) SendFriendAccept(from, to *store.User) {
	s.enqueue(to.RemoteHost, message{Type: "friend_accept", From: person(from), To: to.LocalPart()})
}

// SendRename tells the servers of a person's remote friends that their username
// changed, so the friendship follows the new handle.
func (s *Service) SendRename(u *store.User, oldName string) {
	if u == nil || u.IsRemote() {
		return
	}
	friends, err := s.Store.RemoteFriends(u.ID)
	if err != nil {
		logger.Error("FEDERATION rename notice uid=%d: %v", u.ID, err)
		return
	}
	hosts := map[string]bool{}
	for _, f := range friends {
		hosts[f.Host] = true
	}
	for h := range hosts {
		s.enqueue(h, message{Type: "rename", From: Person{Username: oldName, Name: u.Name()}, NewName: u.Username})
	}
}

func (s *Service) SendFriendEnd(from, to *store.User) {
	s.enqueue(to.RemoteHost, message{Type: "friend_end", From: person(from), To: to.LocalPart()})
}

// Emit shares one piece of a person's activity with their friends on other
// servers. It does nothing for someone with no remote friends or who turned
// sharing off.
func (s *Service) Emit(u *store.User, ev Event) {
	if u == nil || u.IsRemote() || !u.ShareActivity {
		return
	}
	hosts, err := s.audience(u, true)
	if err != nil || len(hosts) == 0 {
		return
	}
	if ev.At == "" {
		ev.At = store.Now()
	}
	for _, h := range hosts {
		e := ev
		s.enqueue(h, message{Type: "activity", From: person(u), Event: &e})
	}
}

// audience is the servers a person's activity goes to: those of their remote
// friends and every allow-listed server. Friendship still decides what anyone
// sees in their feed. friends=false limits it to allow-listed servers.
func (s *Service) audience(u *store.User, friends bool) ([]string, error) {
	set := map[string]bool{}
	if friends {
		fl, err := s.Store.RemoteFriends(u.ID)
		if err != nil {
			logger.Error("FEDERATION audience uid=%d: %v", u.ID, err)
			return nil, err
		}
		for _, f := range fl {
			set[f.Host] = true
		}
	}
	shared, err := s.Store.SharePeers()
	if err != nil {
		logger.Error("FEDERATION audience uid=%d: %v", u.ID, err)
		return nil, err
	}
	for _, h := range shared {
		set[h] = true
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	return out, nil
}

// ShareComment sends an episode comment (new or edited) to every allow-listed
// server. The copies are leased: they lapse after a day unless the home server
// keeps confirming them.
func (s *Service) ShareComment(u *store.User, c CommentMsg) {
	if u == nil || u.IsRemote() || u.IsDeactivated || !u.ShareActivity {
		return
	}
	hosts, err := s.audience(u, false)
	if err != nil {
		return
	}
	for _, h := range hosts {
		cc := c
		s.enqueue(h, message{Type: "comment", From: person(u), Comment: &cc})
	}
}

// WithdrawComment takes a comment back from every allow-listed server.
func (s *Service) WithdrawComment(u *store.User, ref string) {
	if u == nil || u.IsRemote() {
		return
	}
	hosts, err := s.audience(u, false)
	if err != nil {
		return
	}
	for _, h := range hosts {
		s.enqueue(h, message{Type: "comment_retract", From: person(u), Ref: ref})
	}
}

// ForwardReport tells a comment's home server that someone here reported it.
func (s *Service) ForwardReport(reporter *store.User, host, ref, reason string) {
	if reporter == nil || reporter.IsRemote() {
		return
	}
	s.enqueue(host, message{Type: "report", From: person(reporter), Ref: ref, Reason: reason})
}

func (s *Service) Retract(u *store.User, ref string) {
	if u == nil || u.IsRemote() {
		return
	}
	friends, err := s.Store.RemoteFriends(u.ID)
	if err != nil || len(friends) == 0 {
		return
	}
	hosts := map[string]bool{}
	for _, f := range friends {
		hosts[f.Host] = true
	}
	for h := range hosts {
		s.enqueue(h, message{Type: "retract", From: person(u), Ref: ref})
	}
}

func (s *Service) post(ctx context.Context, host, path string, body []byte) (int, error) {
	code, _, err := s.call(ctx, host, path, body)
	return code, err
}

// call posts a signed message and returns the status and the (bounded) reply.
func (s *Service) call(ctx context.Context, host, path string, body []byte) (int, []byte, error) {
	if _, err := s.peerKey(ctx, host); err != nil {
		return 0, nil, err
	}
	date := strconv.FormatInt(time.Now().Unix(), 10)
	digest := digestOf(body)
	sig := ed25519.Sign(s.priv, canonical(http.MethodPost, path, host, date, digest))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.scheme()+"://"+host+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TVT-Host", s.Host)
	req.Header.Set("X-TVT-Date", date)
	req.Header.Set("X-TVT-Digest", digest)
	req.Header.Set("X-TVT-Signature", base64.StdEncoding.EncodeToString(sig))
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	reply, err := readUpTo(resp.Body, maxReply)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, reply, nil
}

// Run delivers queued messages, retrying with growing delays, and clears
// expired leases. It returns when ctx ends.
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	purge := time.NewTicker(time.Hour)
	renew := time.NewTicker(30 * time.Minute)
	defer tick.Stop()
	defer purge.Stop()
	defer renew.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.Flush(ctx)
		case <-renew.C:
			s.RenewLeases(ctx)
		case <-purge.C:
			if err := s.Store.PurgeExpiredRemote(); err != nil {
				logger.Error("FEDERATION %v", err)
			}
			if err := s.Store.PurgeSignatures(30 * time.Minute); err != nil {
				logger.Error("FEDERATION %v", err)
			}
		}
	}
}

// Flush attempts every due message once.
func (s *Service) Flush(ctx context.Context) {
	items, err := s.Store.DueOutbox(25)
	if err != nil {
		logger.Error("FEDERATION %v", err)
		return
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		code, err := s.post(cctx, it.Host, it.Path, []byte(it.Body))
		cancel()
		switch {
		case err == nil && code >= 200 && code < 300:
			if e := s.Store.OutboxDone(it.ID); e != nil {
				logger.Error("FEDERATION %v", e)
			}
		case err == nil && code >= 400 && code < 500 && code != http.StatusTooManyRequests:
			logger.Warn("FEDERATION %s refused message %d with status %d, dropping it", it.Host, it.ID, code)
			if e := s.Store.OutboxDone(it.ID); e != nil {
				logger.Error("FEDERATION %v", e)
			}
		case errors.Is(err, ErrBadHost) || errors.Is(err, ErrKeyChanged):
			logger.Warn("FEDERATION giving up on %s: %v", it.Host, err)
			if e := s.Store.OutboxDone(it.ID); e != nil {
				logger.Error("FEDERATION %v", e)
			}
		default:
			attempts := it.Attempts + 1
			if attempts >= 8 {
				logger.Warn("FEDERATION giving up on message %d to %s after %d attempts: %v status=%d", it.ID, it.Host, attempts, err, code)
				if e := s.Store.OutboxDone(it.ID); e != nil {
					logger.Error("FEDERATION %v", e)
				}
				continue
			}
			wait := time.Duration(1<<attempts) * 30 * time.Second
			if wait > 6*time.Hour {
				wait = 6 * time.Hour
			}
			logger.Warn("FEDERATION message %d to %s failed (attempt %d): %v status=%d", it.ID, it.Host, attempts, err, code)
			if e := s.Store.OutboxRetry(it.ID, attempts, wait); e != nil {
				logger.Error("FEDERATION %v", e)
			}
		}
	}
}
