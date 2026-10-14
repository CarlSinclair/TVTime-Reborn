package federation

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"tvt/logger"
)

var (
	originRe  = regexp.MustCompile(`^https?://[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?$`)
	picPathRe = regexp.MustCompile(`^(/[A-Za-z0-9._~-]{1,64}){0,3}/pics/[0-9]{1,18}-[0-9a-f]{16}\.(jpg|png|gif)$`)

	mediaCheckMu sync.Mutex
	mediaChecked = map[string]time.Time{}
)

// validOrigin accepts a bare scheme://host[:port], the only shape that is safe
// to put in a content security policy.
func validOrigin(o string, insecure bool) bool {
	if !originRe.MatchString(o) {
		return false
	}
	return insecure || strings.HasPrefix(o, "https://")
}

// refreshMedia asks a server where it serves its pictures from, at most once
// every ten minutes and only if its key is still the one pinned.
func (s *Service) refreshMedia(ctx context.Context, host string) {
	mediaCheckMu.Lock()
	if t, ok := mediaChecked[host]; ok && time.Since(t) < 10*time.Minute {
		mediaCheckMu.Unlock()
		return
	}
	if len(mediaChecked) > 1000 {
		mediaChecked = map[string]time.Time{}
	}
	mediaChecked[host] = time.Now()
	mediaCheckMu.Unlock()
	peer, err := s.Store.PeerByHost(host)
	if err != nil {
		return
	}
	doc, err := s.currentDoc(ctx, host)
	if err != nil {
		logger.Warn("FEDERATION picture host of %s: %v", host, err)
		return
	}
	if doc.Key != peer.PublicKey || !validOrigin(doc.Media, s.Insecure) {
		return
	}
	if err := s.Store.SetPeerMedia(host, doc.Media); err != nil {
		logger.Error("FEDERATION %v", err)
	}
}

// imageFrom returns the picture address a server sent with a comment if it
// points at the picture host that server is known to use, otherwise "".
func (s *Service) imageFrom(ctx context.Context, host, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 400 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != s.scheme() || !picPathRe.MatchString(u.Path) {
		return ""
	}
	origin := u.Scheme + "://" + strings.ToLower(u.Host)
	known, _, err := s.Store.PeerMedia(host)
	if err != nil {
		logger.Error("FEDERATION %v", err)
		return ""
	}
	if known != origin {
		s.refreshMedia(ctx, host)
		if known, _, err = s.Store.PeerMedia(host); err != nil || known != origin {
			return ""
		}
	}
	return origin + u.Path
}
