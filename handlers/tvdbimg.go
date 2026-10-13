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
	_, err, _ := imgFlight.Do(key, func() (any, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Cfg.TVDBImageBase+path, nil)
		if err != nil {
			return nil, fmt.Errorf("tvdb artwork request: %w", err)
		}
		resp, err := imgHTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("tvdb artwork fetch %s: %w", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, errArtworkMissing
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("tvdb artwork fetch %s: status %d", path, resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxArtwork+1))
		if err != nil {
			return nil, fmt.Errorf("tvdb artwork read %s: %w", path, err)
		}
		if len(body) > maxArtwork {
			return nil, fmt.Errorf("tvdb artwork %s exceeds %d bytes", path, maxArtwork)
		}
		ct := http.DetectContentType(body)
		if ct != "image/jpeg" && ct != "image/png" && ct != "image/webp" {
			return nil, fmt.Errorf("tvdb artwork %s is not an image (%s)", path, ct)
		}
		if err := a.Media.Put(ctx, key, body, ct); err != nil {
			return nil, fmt.Errorf("tvdb artwork store %s: %w", key, err)
		}
		return nil, nil
	})
	return err
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
