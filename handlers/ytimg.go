package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"tvt/auth"
	"tvt/logger"
	"tvt/media"
	"tvt/youtube"
)

// serveYTImage serves a YouTube video thumbnail (/ytimg/v/<video id>) or a
// channel avatar (/ytimg/c/<encoded avatar path>) through the same cache as
// every other artwork, so no visitor's browser ever contacts YouTube.
func (a *App) serveYTImage(w http.ResponseWriter, r *http.Request) {
	kind, file := r.PathValue("kind"), r.PathValue("file")
	var urls []string
	switch kind {
	case "v":
		if !youtube.ValidVideoID(file) {
			http.NotFound(w, r)
			return
		}
		urls = youtube.ThumbnailURLs(file)
	case "c":
		u, ok := youtube.AvatarURL(file)
		if !ok {
			http.NotFound(w, r)
			return
		}
		urls = []string{u}
	default:
		http.NotFound(w, r)
		return
	}
	if !a.imgLimit.Allow("img:" + auth.ClientIP(r)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	sum := sha256.Sum256([]byte(kind + ":" + file))
	key := "yt/" + hex.EncodeToString(sum[:16]) + ".img"
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
	if err := a.fetchRemoteArtwork(ctx, urls, key); err != nil {
		if errors.Is(err, errArtworkMissing) {
			imgMarkMissed(key)
			http.NotFound(w, r)
			return
		}
		logger.Error("IMG youtube %s/%s: %v", kind, file, err)
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	a.respondArtwork(w, r, key)
}
