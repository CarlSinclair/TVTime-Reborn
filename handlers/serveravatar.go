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
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"

	"tvt/logger"
)

const (
	avatarKV   = "branding.avatar"
	avatarSide = 256
)

var (
	ownAvatarRe  = regexp.MustCompile(`^avatar-[0-9a-f]{16}\.png$`)
	peerAvatarRe = regexp.MustCompile(`^peer-[0-9a-f]{16}\.png$`)
)

func (a *App) ownAvatarFile() string {
	f := a.Store.KVGet(avatarKV)
	if !ownAvatarRe.MatchString(f) {
		return ""
	}
	return f
}

func (a *App) ownAvatarURL() string {
	f := a.ownAvatarFile()
	if f == "" {
		return ""
	}
	u := a.mediaURL("branding", f)
	if strings.HasPrefix(u, "/") {
		u = strings.TrimRight(a.Cfg.BaseURL, "/") + u
	}
	return u
}

func (a *App) serverAvatarSrc(file string) string {
	if !ownAvatarRe.MatchString(file) && !peerAvatarRe.MatchString(file) {
		return ""
	}
	return a.mediaURL("branding", file)
}

func processAvatar(raw []byte) ([]byte, error) {
	if len(raw) > 3<<20 {
		return nil, errBadImage
	}
	decodeSlots <- struct{}{}
	defer func() { <-decodeSlots }()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 16 || cfg.Height < 16 || cfg.Width > 8000 || cfg.Height > 8000 || cfg.Width*cfg.Height > 40_000_000 {
		return nil, errBadImage
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errBadImage
	}
	b := img.Bounds()
	side := b.Dx()
	if b.Dy() < side {
		side = b.Dy()
	}
	crop := image.Rect(b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2, b.Min.X+(b.Dx()-side)/2+side, b.Min.Y+(b.Dy()-side)/2+side)
	out := image.NewNRGBA(image.Rect(0, 0, avatarSide, avatarSide))
	xdraw.CatmullRom.Scale(out, out.Bounds(), img, crop, xdraw.Over, nil)
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, fmt.Errorf("encode avatar: %w", err)
	}
	return buf.Bytes(), nil
}

func (a *App) putAvatar(ctx context.Context, prefix string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	name := prefix + hex.EncodeToString(sum[:8]) + ".png"
	if err := a.Media.Put(ctx, "branding/"+name, data, "image/png"); err != nil {
		return "", fmt.Errorf("store avatar %s: %w", name, err)
	}
	return name, nil
}

func (a *App) storePeerAvatar(ctx context.Context, raw []byte) (string, error) {
	data, err := processAvatar(raw)
	if err != nil {
		return "", err
	}
	return a.putAvatar(ctx, "peer-", data)
}

func (a *App) userAvatarAbsURL(name string) string {
	if !mediaFileRe.MatchString(name) {
		return ""
	}
	u := a.mediaURL("avatars", name)
	if strings.HasPrefix(u, "/") {
		u = strings.TrimRight(a.Cfg.BaseURL, "/") + u
	}
	return u
}

func (a *App) storeRemoteUserAvatar(ctx context.Context, userID int64, raw []byte) (string, error) {
	data, err := processImage(bytes.NewReader(raw), mediaSpecs["avatars"])
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := fmt.Sprintf("%d-%s.jpg", userID, hex.EncodeToString(sum[:8]))
	if err := a.Media.Put(ctx, "avatars/"+name, data, "image/jpeg"); err != nil {
		return "", fmt.Errorf("store avatar %s: %w", name, err)
	}
	return name, nil
}

func (a *App) deleteRemoteUserAvatar(ctx context.Context, name string) error {
	if !mediaFileRe.MatchString(name) {
		return nil
	}
	return a.Media.Delete(ctx, "avatars/"+name)
}

func (a *App) deletePeerAvatar(ctx context.Context, name string) error {
	if !peerAvatarRe.MatchString(name) {
		return nil
	}
	inUse, err := a.Store.PeerAvatarInUse(name)
	if err != nil || inUse {
		return err
	}
	return a.Media.Delete(ctx, "branding/"+name)
}

func (a *App) announceProfile() bool {
	if a.Fed == nil {
		return true
	}
	if err := a.Fed.SendInstanceName(); err != nil {
		logger.Error("FEDERATION avatar change could not be announced: %v", err)
		return false
	}
	return true
}

func (a *App) apiAdminAvatarUpload(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	file, _, err := r.FormFile("file")
	if err != nil {
		a.fail(w, r, errBadImage)
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 3<<20+1))
	if err != nil {
		a.fail(w, r, errBadImage)
		return
	}
	data, err := processAvatar(raw)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	name, err := a.putAvatar(ctx, "avatar-", data)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	old := a.ownAvatarFile()
	if err := a.Store.KVSet(avatarKV, name); err != nil {
		a.fail(w, r, err)
		return
	}
	if old != "" && old != name {
		if err := a.Media.Delete(ctx, "branding/"+old); err != nil {
			logger.Warn("MEDIA remove old server avatar %s: %v", old, err)
		}
	}
	logger.Info("ADMIN uid=%d set the server avatar %s", admin.ID, name)
	a.okJSON(w, map[string]any{"announced": a.announceProfile()})
}

func (a *App) apiAdminAvatarRemove(w http.ResponseWriter, r *http.Request) {
	admin, err := a.needAdmin(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	old := a.ownAvatarFile()
	if err := a.Store.KVDelete(avatarKV); err != nil {
		a.fail(w, r, err)
		return
	}
	if old != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := a.Media.Delete(ctx, "branding/"+old); err != nil {
			logger.Warn("MEDIA remove server avatar %s: %v", old, err)
		}
	}
	logger.Info("ADMIN uid=%d removed the server avatar", admin.ID)
	a.okJSON(w, map[string]any{"announced": a.announceProfile()})
}
