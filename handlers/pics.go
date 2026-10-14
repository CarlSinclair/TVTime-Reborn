package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"

	"tvt/logger"
	"tvt/media"
	"tvt/store"
)

const (
	maxPicSide   = 1280
	maxGifFrames = 300
	maxGifBytes  = 5 << 20
	maxPngBytes  = 3 << 20
)

var (
	picFileRe  = regexp.MustCompile(`^[0-9]+-[0-9a-f]{16}\.(jpg|png|gif)$`)
	stillFile  = regexp.MustCompile(`^/[A-Za-z0-9_-]{8,64}\.(jpg|jpeg|png|webp)$`)
	errPicSize = apiErr{http.StatusBadRequest, "err.image_invalid"}
)

// processPic turns an upload into the file that is stored: a GIF is kept as it
// is after checking it decodes and stays small, anything else is decoded, scaled
// down and re-encoded, which drops metadata and any non-image payload.
func processPic(raw []byte) (data []byte, ext string, err error) {
	if len(raw) > maxUpload {
		return nil, "", errPicSize
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 8 || cfg.Height < 8 || cfg.Width > 8000 || cfg.Height > 8000 || cfg.Width*cfg.Height > 40_000_000 {
		return nil, "", errBadImage
	}
	if format == "gif" {
		if len(raw) > maxGifBytes || cfg.Width > maxPicSide || cfg.Height > maxPicSide {
			return nil, "", errPicSize
		}
		g, err := gif.DecodeAll(bytes.NewReader(raw))
		if err != nil || len(g.Image) == 0 || len(g.Image) > maxGifFrames {
			return nil, "", errBadImage
		}
		return raw, "gif", nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", errBadImage
	}
	b := img.Bounds()
	if b.Dx() > maxPicSide || b.Dy() > maxPicSide {
		w, h := maxPicSide, b.Dy()*maxPicSide/b.Dx()
		if b.Dy() > b.Dx() {
			w, h = b.Dx()*maxPicSide/b.Dy(), maxPicSide
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	if format == "png" {
		if err := png.Encode(&buf, img); err != nil {
			return nil, "", fmt.Errorf("encode png: %w", err)
		}
		if buf.Len() <= maxPngBytes {
			return buf.Bytes(), "png", nil
		}
		buf.Reset()
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 86}); err != nil {
		return nil, "", fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), "jpg", nil
}

// storePic saves a processed picture under the uploader's id and returns its name.
func (a *App) storePic(ctx context.Context, userID int64, raw []byte) (string, error) {
	data, ext, err := processPic(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := fmt.Sprintf("%d-%s.%s", userID, hex.EncodeToString(sum[:8]), ext)
	ct := map[string]string{"jpg": "image/jpeg", "png": "image/png", "gif": "image/gif"}[ext]
	if err := a.Media.Put(ctx, "pics/"+name, data, ct); err != nil {
		return "", fmt.Errorf("store picture for uid=%d: %w", userID, err)
	}
	return name, nil
}

func (a *App) picURL(name string) string { return a.mediaURL("pics", name) }

func (a *App) apiUploadPic(w http.ResponseWriter, r *http.Request) {
	u, err := a.needVerified(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.reportLimit, "pic:"+itoa(u.ID)); err != nil {
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
	raw, err := io.ReadAll(io.LimitReader(file, maxUpload+1))
	if err != nil {
		a.fail(w, r, errBadImage)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	name, err := a.storePic(ctx, u.ID, raw)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"name": name, "url": a.picURL(name)})
}

type stillsEntry struct {
	at    time.Time
	paths []string
}

var (
	stillsMu    sync.Mutex
	stillsCache = map[int64]stillsEntry{}
)

// episodeStills lists the stills providers have for an episode: the one already
// on file first, then everything TMDB offers. The result is kept for six hours.
func (a *App) episodeStills(ctx context.Context, ep *store.Episode) ([]string, error) {
	stillsMu.Lock()
	if e, ok := stillsCache[ep.ID]; ok && time.Since(e.at) < 6*time.Hour {
		stillsMu.Unlock()
		return e.paths, nil
	}
	stillsMu.Unlock()
	sh, err := a.Store.ShowByID(ep.ShowID)
	if err != nil {
		return nil, err
	}
	var paths []string
	seen := map[string]bool{}
	add := func(p string) {
		if stillFile.MatchString(p) && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	add(ep.StillPath)
	if a.TMDB != nil && a.TMDB.Configured() {
		list, err := a.TMDB.EpisodeStills(ctx, sh.TMDBID, ep.SeasonNumber, ep.EpisodeNumber)
		if err != nil {
			logger.Warn("PICS stills for episode=%d from TMDB: %v", ep.ID, err)
		}
		for _, p := range list {
			add(p)
		}
	}
	if len(paths) > 40 {
		paths = paths[:40]
	}
	stillsMu.Lock()
	if len(stillsCache) > 2000 {
		stillsCache = map[int64]stillsEntry{}
	}
	stillsCache[ep.ID] = stillsEntry{at: time.Now(), paths: paths}
	stillsMu.Unlock()
	return paths, nil
}

func (a *App) apiEpisodeStills(w http.ResponseWriter, r *http.Request) {
	if _, err := a.needVerified(r); err != nil {
		a.fail(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.fail(w, r, errNotFound)
		return
	}
	ep, err := a.Store.Episode(id, 0)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	paths, err := a.episodeStills(ctx, ep)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"stills": paths})
}

// apiPicFromStill copies one of an episode's stills into the person's own
// uploads, so a comment shows a picture served from this site.
func (a *App) apiPicFromStill(w http.ResponseWriter, r *http.Request) {
	u, err := a.needVerified(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.reportLimit, "pic:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Episode int64
		File    string
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	ep, err := a.Store.Episode(in.Episode, 0)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	paths, err := a.episodeStills(ctx, ep)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	listed := false
	for _, p := range paths {
		listed = listed || p == in.File
	}
	if !listed {
		a.fail(w, r, errBadRequest)
		return
	}
	file := strings.TrimPrefix(in.File, "/")
	key := "tmdb/w780/" + file
	if _, err := a.Media.Get(ctx, key); err != nil {
		if !errors.Is(err, media.ErrNotFound) {
			a.fail(w, r, err)
			return
		}
		if err := a.fetchArtwork(ctx, "w780", file, key); err != nil {
			logger.Error("PICS fetch still %s for uid=%d: %v", file, u.ID, err)
			a.fail(w, r, apiErr{http.StatusBadGateway, "err.image_invalid"})
			return
		}
	}
	obj, err := a.Media.Get(ctx, key)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	raw, rerr := io.ReadAll(io.LimitReader(obj.Body, maxArtwork+1))
	obj.Body.Close()
	if rerr != nil {
		a.fail(w, r, rerr)
		return
	}
	name, err := a.storePic(ctx, u.ID, raw)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"name": name, "url": a.picURL(name)})
}

// ownPic checks that a picture name was uploaded by this person.
func ownPic(name string, userID int64) bool {
	return picFileRe.MatchString(name) && strings.HasPrefix(name, strconv.FormatInt(userID, 10)+"-")
}

// dropPic removes a picture file once no comment shows it any more.
func (a *App) dropPic(name string) {
	if name == "" || !picFileRe.MatchString(name) {
		return
	}
	used, err := a.Store.ImageInUse(name)
	if err != nil {
		logger.Error("PICS check use of %s: %v", name, err)
		return
	}
	if used {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.Media.Delete(ctx, "pics/"+name); err != nil {
		logger.Warn("PICS remove %s: %v", name, err)
	}
}
