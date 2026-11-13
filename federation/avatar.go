package federation

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"tvt/logger"
)

var avatarPathRe = regexp.MustCompile(`^(/[A-Za-z0-9._~-]{1,64}){0,3}/branding/avatar-[0-9a-f]{16}\.png$`)

const maxAvatarBytes = 1 << 20

// applyProfile records what a server says about itself: its name, where its
// pictures live and its avatar. Each part is kept only when valid; a bad part
// never blocks the others.
func (s *Service) applyProfile(ctx context.Context, host string, doc *wellKnown) {
	if name, err := CleanInstanceName(doc.Name); err == nil {
		if err := s.Store.SetPeerName(host, name); err != nil {
			logger.Error("FEDERATION %v", err)
		}
	}
	if !validOrigin(doc.Media, s.Insecure) {
		return
	}
	if err := s.Store.SetPeerMedia(host, doc.Media); err != nil {
		logger.Error("FEDERATION %v", err)
	}
	s.refreshAvatar(ctx, host, doc)
}

// refreshProfile re-reads a server's well-known document and applies it, if
// its key is still the pinned one.
func (s *Service) refreshProfile(ctx context.Context, host string) {
	peer, err := s.Store.PeerByHost(host)
	if err != nil {
		return
	}
	doc, err := s.currentDoc(ctx, host)
	if err != nil {
		logger.Warn("FEDERATION profile refresh of %s: %v", host, err)
		return
	}
	if doc.Key != peer.PublicKey {
		return
	}
	s.applyProfile(ctx, host, doc)
}

func (s *Service) refreshAvatar(ctx context.Context, host string, doc *wellKnown) {
	if s.StoreAvatar == nil {
		return
	}
	oldFile, source := s.Store.PeerAvatar(host)
	if doc.Avatar == "" {
		if source != "" {
			if err := s.Store.ClearPeerAvatar(host); err != nil {
				logger.Error("FEDERATION %v", err)
				return
			}
			s.dropAvatarFile(ctx, host, oldFile)
		}
		return
	}
	if doc.Avatar == source {
		return
	}
	u, err := url.Parse(doc.Avatar)
	if err != nil || len(doc.Avatar) > 400 || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != s.scheme() ||
		strings.ToLower(u.Scheme+"://"+u.Host) != strings.ToLower(doc.Media) || !avatarPathRe.MatchString(u.Path) {
		logger.Warn("FEDERATION %s published an avatar address that is not accepted", host)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.Avatar, nil)
	if err != nil {
		logger.Warn("FEDERATION avatar request for %s: %v", host, err)
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		logger.Warn("FEDERATION avatar download from %s: %v", host, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Warn("FEDERATION avatar download from %s: status %d", host, resp.StatusCode)
		return
	}
	raw, err := readUpTo(resp.Body, maxAvatarBytes)
	if err != nil {
		logger.Warn("FEDERATION avatar from %s: %v", host, err)
		return
	}
	name, err := s.StoreAvatar(ctx, raw)
	if err != nil {
		logger.Warn("FEDERATION avatar from %s rejected: %v", host, err)
		return
	}
	if err := s.Store.SetPeerAvatar(host, name, doc.Avatar); err != nil {
		logger.Error("FEDERATION %v", fmt.Errorf("keep avatar of %s: %w", host, err))
		s.dropAvatarFile(ctx, host, name)
		return
	}
	if oldFile != name {
		s.dropAvatarFile(ctx, host, oldFile)
	}
}

func (s *Service) dropAvatarFile(ctx context.Context, host, name string) {
	if name == "" || s.DeleteAvatar == nil {
		return
	}
	if err := s.DeleteAvatar(ctx, name); err != nil {
		logger.Warn("FEDERATION could not remove old avatar %s of %s: %v", name, host, err)
	}
}
