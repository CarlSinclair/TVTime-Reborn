package federation

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"tvt/logger"
	"tvt/store"
)

var userAvatarPathRe = regexp.MustCompile(`^(/[A-Za-z0-9._~-]{1,64}){0,3}/avatars/[0-9]{1,18}-[0-9a-f]{16}\.jpg$`)

const userAvatarSourceKey = "user_avatar_src:"

var (
	userCheckMu sync.Mutex
	userChecked = map[string]time.Time{}
)

func (s *Service) applyUserAvatar(ctx context.Context, host string, u *store.User, avatar string) {
	if s.StoreUserAvatar == nil {
		return
	}
	key := userAvatarSourceKey + strconv.FormatInt(u.ID, 10)
	if avatar == "" {
		if u.AvatarPath != "" {
			s.dropUserAvatar(ctx, u, "")
		}
		return
	}
	if s.Store.KVGet(key) == avatar && u.AvatarPath != "" {
		return
	}
	origin, _, err := s.Store.PeerMedia(host)
	if err != nil {
		logger.Warn("FEDERATION user avatar of %s: %v", u.Username, err)
		return
	}
	pu, perr := url.Parse(avatar)
	if origin == "" || perr != nil || len(avatar) > 400 || pu.User != nil || pu.RawQuery != "" || pu.Fragment != "" ||
		strings.ToLower(pu.Scheme+"://"+pu.Host) != strings.ToLower(origin) || !userAvatarPathRe.MatchString(pu.Path) {
		logger.Warn("FEDERATION %s published an avatar address for %s that is not accepted", host, u.Username)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, avatar, nil)
	if err != nil {
		logger.Warn("FEDERATION avatar request for %s: %v", u.Username, err)
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		logger.Warn("FEDERATION avatar download for %s: %v", u.Username, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Warn("FEDERATION avatar download for %s: status %d", u.Username, resp.StatusCode)
		return
	}
	raw, err := readUpTo(resp.Body, maxAvatarBytes)
	if err != nil {
		logger.Warn("FEDERATION avatar for %s: %v", u.Username, err)
		return
	}
	name, err := s.StoreUserAvatar(ctx, u.ID, raw)
	if err != nil {
		logger.Warn("FEDERATION avatar for %s rejected: %v", u.Username, err)
		return
	}
	if err := s.Store.SetAvatar(u.ID, name); err != nil {
		logger.Error("FEDERATION keep avatar of %s: %v", u.Username, err)
		s.dropFile(ctx, u.Username, name)
		return
	}
	if err := s.Store.KVSet(key, avatar); err != nil {
		logger.Error("FEDERATION remember avatar source of %s: %v", u.Username, err)
	}
	if u.AvatarPath != "" && u.AvatarPath != name {
		s.dropFile(ctx, u.Username, u.AvatarPath)
	}
	u.AvatarPath = name
}

func (s *Service) dropUserAvatar(ctx context.Context, u *store.User, keep string) {
	old := u.AvatarPath
	if err := s.Store.SetAvatar(u.ID, keep); err != nil {
		logger.Error("FEDERATION clear avatar of %s: %v", u.Username, err)
		return
	}
	if err := s.Store.KVDelete(userAvatarSourceKey + strconv.FormatInt(u.ID, 10)); err != nil {
		logger.Error("FEDERATION forget avatar source of %s: %v", u.Username, err)
	}
	u.AvatarPath = keep
	s.dropFile(ctx, u.Username, old)
}

func (s *Service) dropFile(ctx context.Context, who, name string) {
	if name == "" || s.DeleteUserAvatar == nil {
		return
	}
	if err := s.DeleteUserAvatar(ctx, name); err != nil {
		logger.Warn("FEDERATION could not remove avatar file %s of %s: %v", name, who, err)
	}
}

// RefreshUser re-reads what a person's own server says about them, at most
// once an hour per person, and updates the stored copy.
func (s *Service) RefreshUser(ctx context.Context, u *store.User) {
	if !u.IsRemote() {
		return
	}
	userCheckMu.Lock()
	if t, ok := userChecked[u.Username]; ok && time.Since(t) < time.Hour {
		userCheckMu.Unlock()
		return
	}
	if len(userChecked) > 5000 {
		userChecked = map[string]time.Time{}
	}
	userChecked[u.Username] = time.Now()
	userCheckMu.Unlock()
	if _, err := s.ResolveUser(ctx, u.LocalPart(), u.RemoteHost); err != nil {
		logger.Warn("FEDERATION refresh of %s: %v", u.Username, err)
	}
}
