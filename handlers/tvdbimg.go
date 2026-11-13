package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"tvt/auth"
	"tvt/logger"
	"tvt/media"
)

var tvdbPathRe = regexp.MustCompile(`^/banners/[A-Za-z0-9/_.-]{3,200}\.(jpg|jpeg|png|webp)$`)

func decodeTVDBPath(file string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(file)
	if err != nil {
		return "", false
	}
	p := string(raw)
	if !tvdbPathRe.MatchString(p) || containsDotDot(p) {
		return "", false
	}
	return p, true
}

func containsDotDot(p string) bool {
	for i := 0; i+1 < len(p); i++ {
		if p[i] == '.' && p[i+1] == '.' {
			return true
		}
	}
	return false
}

func (a *App) fetchTVDBArtwork(ctx context.Context, path, key string) error {
	return a.fetchRemoteArtwork(ctx, []string{a.Cfg.TVDBImageBase + path}, key)
}

func (a *App) fetchRemoteArtwork(ctx context.Context, urls []string, key string) error {
	_, err, _ := imgFlight.Do(key, func() (any, error) {
		for i, u := range urls {
			err := a.fetchOneArtwork(ctx, u, key)
			if errors.Is(err, errArtworkMissing) && i < len(urls)-1 {
				continue
			}
			return nil, err
		}
		return nil, errArtworkMissing
	})
	return err
}

func (a *App) fetchOneArtwork(ctx context.Context, u, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("artwork request: %w", err)
	}
	resp, err := imgHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("artwork fetch %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errArtworkMissing
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("artwork fetch %s: status %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArtwork+1))
	if err != nil {
		return fmt.Errorf("artwork read %s: %w", u, err)
	}
	if len(body) > maxArtwork {
		return fmt.Errorf("artwork %s exceeds %d bytes", u, maxArtwork)
	}
	ct := http.DetectContentType(body)
	if ct != "image/jpeg" && ct != "image/png" && ct != "image/webp" {
		return fmt.Errorf("artwork %s is not an image (%s)", u, ct)
	}
	if err := a.Media.Put(ctx, key, body, ct); err != nil {
		return fmt.Errorf("artwork store %s: %w", key, err)
	}
	return nil
}

func (a *App) serveTVDBImage(w http.ResponseWriter, r *http.Request) {
	path, ok := decodeTVDBPath(r.PathValue("file"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !a.imgLimit.Allow("img:" + auth.ClientIP(r)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	sum := sha256.Sum256([]byte(path))
	key := "tvdb/" + hex.EncodeToString(sum[:16]) + ".img"
	if obj, err := a.Media.Get(r.Context(), key); err == nil {
		obj.Body.Close()
		a.respondArtwork(w, r, key)
		return
	} else if !errors.Is(err, media.ErrNotFound) {
		logger.Error("IMG cache lookup %s: %v", key, err)
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	if imgMissed(key) {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := a.fetchTVDBArtwork(ctx, path, key); err != nil {
		if errors.Is(err, errArtworkMissing) {
			imgMarkMissed(key)
			http.NotFound(w, r)
			return
		}
		logger.Error("IMG %v", err)
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	a.respondArtwork(w, r, key)
}
