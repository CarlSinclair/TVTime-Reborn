package handlers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"tvt/logger"
	"tvt/media"
	"tvt/store"
)

const maxUpload = 6 << 20

var (
	mediaFileRe = regexp.MustCompile(`^[0-9]+-[0-9a-f]{16}\.jpg$`)
	errBadImage = apiErr{http.StatusBadRequest, "err.image_invalid"}
)

type mediaSpec struct{ w, h int }

var mediaSpecs = map[string]mediaSpec{"avatars": {256, 256}, "banners": {1500, 500}}

// processImage decodes an upload, centre-crops it to the target aspect ratio,
// scales it down and re-encodes as JPEG. Re-encoding drops metadata and any
// non-image payload, and the size check runs before the full decode so a
// decompression bomb cannot exhaust memory.
func processImage(src io.Reader, spec mediaSpec) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(src, maxUpload+1))
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err)
	}
	if len(raw) > maxUpload {
		return nil, errBadImage
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 16 || cfg.Height < 16 || cfg.Width > 8000 || cfg.Height > 8000 || cfg.Width*cfg.Height > 40_000_000 {
		return nil, errBadImage
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errBadImage
	}
	b := img.Bounds()
	wantRatio := float64(spec.w) / float64(spec.h)
	cw, ch := b.Dx(), b.Dy()
	if float64(cw)/float64(ch) > wantRatio {
		cw = int(float64(ch) * wantRatio)
	} else {
		ch = int(float64(cw) / wantRatio)
	}
	crop := image.Rect(b.Min.X+(b.Dx()-cw)/2, b.Min.Y+(b.Dy()-ch)/2, b.Min.X+(b.Dx()-cw)/2+cw, b.Min.Y+(b.Dy()-ch)/2+ch)
	out := image.NewRGBA(image.Rect(0, 0, spec.w, spec.h))
	draw.Draw(out, out.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(out, out.Bounds(), img, crop, xdraw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

func (a *App) setMediaPath(userID int64, kind, name string) error {
	if kind == "avatars" {
		return a.Store.SetAvatar(userID, name)
	}
	return a.Store.SetBanner(userID, name)
}

func currentMedia(u *store.User, kind string) string {
	if kind == "avatars" {
		return u.AvatarPath
	}
	return u.BannerPath
}

func (a *App) apiUploadMedia(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := a.needUser(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if err := a.allow(a.reportLimit, "up:"+itoa(u.ID)); err != nil {
			a.fail(w, r, err)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload+(1<<20))
		file, _, err := r.FormFile("file")
		if err != nil {
			a.fail(w, r, errBadImage)
			return
		}
		defer file.Close()
		data, err := processImage(file, mediaSpecs[kind])
		if err != nil {
			a.fail(w, r, err)
			return
		}
		sum := sha256.Sum256(data)
		name := fmt.Sprintf("%d-%s.jpg", u.ID, hex.EncodeToString(sum[:8]))
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := a.Media.Put(ctx, kind+"/"+name, data, "image/jpeg"); err != nil {
			a.fail(w, r, fmt.Errorf("store %s for uid=%d: %w", kind, u.ID, err))
			return
		}
		old := currentMedia(u, kind)
		if err := a.setMediaPath(u.ID, kind, name); err != nil {
			a.fail(w, r, err)
			return
		}
		if old != "" && old != name && mediaFileRe.MatchString(old) {
			if err := a.Media.Delete(ctx, kind+"/"+old); err != nil {
				logger.Warn("MEDIA remove old %s/%s: %v", kind, old, err)
			}
		}
		a.okJSON(w, map[string]any{"url": "/media/" + kind + "/" + name})
	}
}

func (a *App) apiRemoveMedia(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := a.needUser(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		old := currentMedia(u, kind)
		if err := a.setMediaPath(u.ID, kind, ""); err != nil {
			a.fail(w, r, err)
			return
		}
		if old != "" && mediaFileRe.MatchString(old) {
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			if err := a.Media.Delete(ctx, kind+"/"+old); err != nil {
				logger.Warn("MEDIA remove %s/%s: %v", kind, old, err)
			}
		}
		a.okJSON(w, nil)
	}
}

func (a *App) removeUserMedia(ctx context.Context, u *store.User) error {
	var firstErr error
	for kind, name := range map[string]string{"avatars": u.AvatarPath, "banners": u.BannerPath} {
		if name == "" || !mediaFileRe.MatchString(name) {
			continue
		}
		if err := a.Media.Delete(ctx, kind+"/"+name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *App) serveMedia(w http.ResponseWriter, r *http.Request) {
	kind, file := r.PathValue("kind"), r.PathValue("file")
	if _, ok := mediaSpecs[kind]; !ok || !mediaFileRe.MatchString(file) {
		http.NotFound(w, r)
		return
	}
	if u := a.Media.PublicURL(kind + "/" + file); u != "" {
		http.Redirect(w, r, u, http.StatusFound)
		return
	}
	a.serveObject(w, r, kind+"/"+file)
}

// serveObject streams a stored object with long-lived caching. Filenames are
// content-addressed, so a changed image always gets a new URL.
func (a *App) serveObject(w http.ResponseWriter, r *http.Request, key string) {
	obj, err := a.Media.Get(r.Context(), key)
	if err != nil {
		if errors.Is(err, media.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		logger.Error("MEDIA get %s: %v", key, err)
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	defer obj.Body.Close()
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	br := bufio.NewReader(obj.Body)
	head, _ := br.Peek(512)
	ct := http.DetectContentType(head)
	if ct != "image/jpeg" && ct != "image/png" && ct != "image/webp" && ct != "image/gif" {
		ct = obj.ContentType
	}
	w.Header().Set("Content-Type", ct)
	if obj.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	}
	if _, err := io.Copy(w, br); err != nil {
		logger.Warn("MEDIA stream %s: %v", key, err)
	}
}

// mediaURL is the address browsers use for an uploaded image: straight from the
// public bucket domain when one is configured, otherwise through this app.
func (a *App) mediaURL(kind, name string) string {
	if name == "" {
		return ""
	}
	if u := a.Media.PublicURL(kind + "/" + name); u != "" {
		return u
	}
	return "/media/" + kind + "/" + name
}

func (a *App) publicMediaOrigin() string {
	if a.Cfg.MediaPublicURL == "" {
		return ""
	}
	u, err := url.Parse(a.Cfg.MediaPublicURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return " " + u.Scheme + "://" + u.Host
}
