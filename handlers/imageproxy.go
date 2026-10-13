package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	"tvt/auth"
	"tvt/logger"
	"tvt/media"

	"golang.org/x/sync/singleflight"
)

// Artwork is served from our own origin and fetched from TMDB's CDN only the
// first time it is requested, so a visitor's browser never contacts TMDB and
// the cache fills with just what people actually look at.
var (
	tmdbSizes   = map[string]bool{"w92": true, "w154": true, "w185": true, "w300": true, "w342": true, "w500": true, "w780": true}
	imgFileRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}\.(jpg|jpeg|png|webp)$`)
	imgFlight   singleflight.Group
	imgMissMu   sync.Mutex
	imgMissSeen = map[string]time.Time{}
	imgHTTP     = &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
)

const maxArtwork = 6 << 20

func imgMissed(key string) bool {
	imgMissMu.Lock()
	defer imgMissMu.Unlock()
	if t, ok := imgMissSeen[key]; ok {
		if time.Since(t) < 10*time.Minute {
			return true
		}
		delete(imgMissSeen, key)
	}
	return false
}

func imgMarkMissed(key string) {
	imgMissMu.Lock()
	defer imgMissMu.Unlock()
	if len(imgMissSeen) > 5000 {
		imgMissSeen = map[string]time.Time{}
	}
	imgMissSeen[key] = time.Now()
}

var errArtworkMissing = errors.New("artwork not found upstream")

func (a *App) fetchArtwork(ctx context.Context, size, file, key string) error {
	_, err, _ := imgFlight.Do(key, func() (any, error) {
		upstream := a.Cfg.ImageBase + "/" + size + "/" + file
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
		if err != nil {
			return nil, fmt.Errorf("artwork request: %w", err)
		}
		resp, err := imgHTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("artwork fetch %s/%s: %w", size, file, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, errArtworkMissing
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("artwork fetch %s/%s: status %d", size, file, resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxArtwork+1))
		if err != nil {
			return nil, fmt.Errorf("artwork read %s/%s: %w", size, file, err)
		}
		if len(body) > maxArtwork {
			return nil, fmt.Errorf("artwork %s/%s exceeds %d bytes", size, file, maxArtwork)
		}
		ct := http.DetectContentType(body)
		if ct != "image/jpeg" && ct != "image/png" && ct != "image/webp" {
			return nil, fmt.Errorf("artwork %s/%s is not an image (%s)", size, file, ct)
		}
		if err := a.Media.Put(ctx, key, body, ct); err != nil {
			return nil, fmt.Errorf("artwork store %s: %w", key, err)
		}
		return nil, nil
	})
	return err
}

func (a *App) serveImage(w http.ResponseWriter, r *http.Request) {
	size, file := r.PathValue("size"), r.PathValue("file")
	if !tmdbSizes[size] || !imgFileRe.MatchString(file) {
		http.NotFound(w, r)
		return
	}
	if !a.imgLimit.Allow("img:" + auth.ClientIP(r)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	key := "tmdb/" + size + "/" + file
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
	if err := a.fetchArtwork(ctx, size, file, key); err != nil {
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

// respondArtwork sends the browser to the public bucket domain when there is
// one (so image bytes never pass through this app), otherwise streams it.
func (a *App) respondArtwork(w http.ResponseWriter, r *http.Request, key string) {
	if u := a.Media.PublicURL(key); u != "" {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.Redirect(w, r, u, http.StatusFound)
		return
	}
	a.serveObject(w, r, key)
}
