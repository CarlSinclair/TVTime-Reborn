package federation

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tvt/logger"
	"tvt/store"
)

func (s *Service) remember(sig string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if len(s.seen) > 20000 {
		for k, t := range s.seen {
			if now.Sub(t) > 2*dateSkew {
				delete(s.seen, k)
			}
		}
	}
	if _, dup := s.seen[sig]; dup {
		return false
	}
	s.seen[sig] = now
	return true
}

// Inbox receives messages from other servers. Every message must carry a
// signature from the sending server's pinned key, over its method, path, the
// receiving host, a fresh timestamp and a digest of the body.
func (s *Service) Inbox(w http.ResponseWriter, r *http.Request) {
	from := strings.ToLower(strings.TrimSpace(r.Header.Get("X-TVT-Host")))
	if !s.inLimit.Allow("in:" + from) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	body, err := readLimited(http.MaxBytesReader(w, r.Body, maxBody+1))
	if err != nil {
		http.Error(w, "bad request", http.StatusRequestEntityTooLarge)
		return
	}
	date := r.Header.Get("X-TVT-Date")
	ts, err := strconv.ParseInt(date, 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)) > dateSkew || time.Until(time.Unix(ts, 0)) > dateSkew {
		http.Error(w, "stale or missing date", http.StatusUnauthorized)
		return
	}
	digest := r.Header.Get("X-TVT-Digest")
	if digest != digestOf(body) {
		http.Error(w, "digest mismatch", http.StatusUnauthorized)
		return
	}
	sig, err := base64.StdEncoding.DecodeString(r.Header.Get("X-TVT-Signature"))
	if err != nil {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	key, err := s.peerKey(r.Context(), from)
	if err != nil {
		logger.Warn("FEDERATION rejected a message from %q: %v", from, err)
		http.Error(w, "unknown or blocked server", http.StatusForbidden)
		return
	}
	if !ed25519.Verify(key, canonical(r.Method, r.URL.Path, s.Host, date, digest), sig) {
		logger.Warn("FEDERATION bad signature from %s", from)
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	if !s.remember(base64.StdEncoding.EncodeToString(sig)) {
		http.Error(w, "replay", http.StatusConflict)
		return
	}
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := s.dispatch(from, m); err != nil {
		if errors.Is(err, errBadMessage) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		logger.Error("FEDERATION handling %s from %s: %v", m.Type, from, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errBadMessage = errors.New("bad message")

func (s *Service) dispatch(host string, m message) error {
	if !nameRe.MatchString(m.From.Username) {
		return errBadMessage
	}
	switch m.Type {
	case "friend_request", "friend_accept", "friend_end":
		return s.onFriend(host, m)
	case "activity":
		return s.onActivity(host, m)
	case "retract":
		return s.onRetract(host, m)
	}
	return errBadMessage
}

func (s *Service) localTarget(name string) (*store.User, error) {
	if !nameRe.MatchString(name) {
		return nil, errBadMessage
	}
	u, err := s.Store.UserByUsername(name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if u.IsRemote() || u.IsBanned {
		return nil, nil
	}
	return u, nil
}

func (s *Service) onFriend(host string, m message) error {
	target, err := s.localTarget(m.To)
	if err != nil || target == nil {
		return err
	}
	shadow, err := s.Store.EnsureRemoteUser(host, m.From.Username, cleanName(m.From.Name))
	if err != nil {
		return err
	}
	if shadow.IsBanned {
		return nil
	}
	if blocked, err := s.Store.BlockedEither(shadow.ID, target.ID); err != nil || blocked {
		return err
	}
	switch m.Type {
	case "friend_request":
		state, err := s.Store.RequestFriend(shadow.ID, target.ID)
		if err != nil && !errors.Is(err, store.ErrAlreadyFriends) {
			return err
		}
		switch state {
		case "outgoing":
			s.Notify.Send(target.ID, "friend_request", "/?friends=1", map[string]any{"user": shadow.Name() + " (" + shadow.Username + ")", "username": shadow.Username})
		case "friends":
			s.SendFriendAccept(target, shadow)
			s.Notify.Send(target.ID, "friend_accepted", "/u/"+shadow.Username, map[string]any{"user": shadow.Name()})
		}
	case "friend_accept":
		if err := s.Store.AcceptFriend(shadow.ID, target.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		s.Notify.Send(target.ID, "friend_accepted", "/u/"+shadow.Username, map[string]any{"user": shadow.Name()})
	case "friend_end":
		if err := s.Store.EndFriendship(target.ID, shadow.ID); err != nil {
			return err
		}
		if ok, err := s.Store.HasLocalFriendOf(shadow.ID); err == nil && !ok {
			return s.Store.DropRemoteEvents(shadow.ID)
		}
	}
	return nil
}

var kinds = map[string]bool{"watch": true, "rating": true, "comment": true, "post": true}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (s *Service) onActivity(host string, m message) error {
	if m.Event == nil || !kinds[m.Event.Kind] || m.Event.Ref == "" || len(m.Event.Ref) > 64 {
		return errBadMessage
	}
	shadow, err := s.Store.UserByUsername(store.RemoteHandle(m.From.Username, host))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if !shadow.IsRemote() || shadow.IsBanned {
		return nil
	}
	if ok, err := s.Store.HasLocalFriendOf(shadow.ID); err != nil || !ok {
		return err
	}
	at := m.Event.At
	if _, err := time.Parse("2006-01-02 15:04:05", at); err != nil {
		at = store.Now()
	}
	ev := store.RemoteEvent{
		Ref: m.Event.Ref, Kind: m.Event.Kind, ShowTMDB: m.Event.ShowTMDB, Show: clip(m.Event.Show, 200), Poster: sanitizePoster(m.Event.Poster),
		Season: clampInt(m.Event.Season, 0, 5000), Episode: clampInt(m.Event.Episode, 0, 100000), Count: clampInt(m.Event.Count, 1, 10000),
		Stars: clampInt(m.Event.Stars, 0, 5), Excerpt: clip(m.Event.Excerpt, 400), At: at,
	}
	return s.Store.SaveRemoteEvent(shadow.ID, ev)
}

func (s *Service) onRetract(host string, m message) error {
	shadow, err := s.Store.UserByUsername(store.RemoteHandle(m.From.Username, host))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if !shadow.IsRemote() || m.Ref == "" || len(m.Ref) > 64 {
		return errBadMessage
	}
	return s.Store.RetractRemoteEvent(shadow.ID, m.Ref)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// sanitizePoster keeps only artwork paths this server knows how to proxy.
func sanitizePoster(p string) string {
	if p == "" || len(p) > 200 {
		return ""
	}
	if strings.HasPrefix(p, "tvdb:/banners/") {
		return p
	}
	if len(p) > 4 && p[0] == '/' && !strings.Contains(p[1:], "/") && imgName(p[1:]) {
		return p
	}
	return ""
}

func imgName(n string) bool {
	if len(n) < 8 || len(n) > 70 {
		return false
	}
	dot := strings.LastIndex(n, ".")
	if dot < 0 {
		return false
	}
	switch strings.ToLower(n[dot+1:]) {
	case "jpg", "jpeg", "png", "webp":
	default:
		return false
	}
	for _, c := range n[:dot] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
