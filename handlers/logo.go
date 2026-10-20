package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"

	"tvt/logger"
)

const (
	logoKV      = "branding.logo"
	logoMaxW    = 640
	logoMaxH    = 160
	defaultLogo = "/static/img/icon.svg"
)

var logoFileRe = regexp.MustCompile(`^logo-[0-9a-f]{16}\.png$`)

// logoState caches which logo file is set, so a page render needs no query.
type logoState struct {
	mu   sync.RWMutex
	file string
}

func (l *logoState) get() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.file
}

func (l *logoState) set(f string) {
	l.mu.Lock()
	l.file = f
	l.mu.Unlock()
}

func (a *App) logoURL() string {
	if f := a.logo.get(); f != "" {
		return a.mediaURL("branding", f)
	}
	return defaultLogo
}

// processLogo decodes an upload and scales it down to fit the top bar,
// keeping its aspect ratio and transparency, as PNG.
func processLogo(src io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(src, 2<<20+1))
	if err != nil {
		return nil, fmt.Errorf("read logo: %w", err)
	}
	if len(raw) > 2<<20 {
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
	w, h := b.Dx(), b.Dy()
	if w > logoMaxW || h > logoMaxH {
		scale := float64(logoMaxW) / float64(w)
		if s := float64(logoMaxH) / float64(h); s < scale {
			scale = s
		}
		w, h = int(float64(w)*scale), int(float64(h)*scale)
		if w < 1 {
			w = 1
		}
		if h < 1 {
			h = 1
		}
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(out, out.Bounds(), img, b, xdraw.Over, nil)
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, fmt.Errorf("encode logo: %w", err)
	}
	return buf.Bytes(), nil
}

func (a *App) apiAdminLogoUpload(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20)
	file, _, err := r.FormFile("file")
	if err != nil {
		a.fail(w, r, errBadImage)
		return
	}
	defer file.Close()
	data, err := processLogo(file)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sum := sha256.Sum256(data)
	name := "logo-" + hex.EncodeToString(sum[:8]) + ".png"
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.Media.Put(ctx, "branding/"+name, data, "image/png"); err != nil {
		a.fail(w, r, fmt.Errorf("store logo: %w", err))
		return
	}
	old := a.logo.get()
	if err := a.Store.KVSet(logoKV, name); err != nil {
		a.fail(w, r, err)
		return
	}
	a.logo.set(name)
	if old != "" && old != name && logoFileRe.MatchString(old) {
		if err := a.Media.Delete(ctx, "branding/"+old); err != nil {
			logger.Warn("MEDIA remove old logo %s: %v", old, err)
		}
	}
	logger.Info("ADMIN uid=%d set the logo %s", admin.ID, name)
	a.okJSON(w, map[string]any{"url": a.logoURL()})
}

func (a *App) apiAdminLogoRemove(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	old := a.logo.get()
	if err := a.Store.KVDelete(logoKV); err != nil {
		a.fail(w, r, err)
		return
	}
	a.logo.set("")
	if old != "" && logoFileRe.MatchString(old) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := a.Media.Delete(ctx, "branding/"+old); err != nil {
			logger.Warn("MEDIA remove logo %s: %v", old, err)
		}
	}
	logger.Info("ADMIN uid=%d reset the logo to the default", admin.ID)
	a.okJSON(w, nil)
}
