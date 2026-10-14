package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"

	"tvt/federation"
	"tvt/logger"
	"tvt/store"
)

const (
	maxPicUpload  = 16 << 20
	maxPicSide    = 1920
	maxGifSide    = 1280
	maxGifFrames  = 600
	maxGifBytes   = 12 << 20
	maxPngBytes   = 4 << 20
	maxStillBytes = 12 << 20
	picKeepFor    = 24 * time.Hour
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
	if len(raw) > maxPicUpload {
		return nil, "", errPicSize
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 8 || cfg.Height < 8 || cfg.Width > 8000 || cfg.Height > 8000 || cfg.Width*cfg.Height > 40_000_000 {
		return nil, "", errBadImage
	}
	if format == "gif" {
		if len(raw) > maxGifBytes || cfg.Width > maxGifSide || cfg.Height > maxGifSide {
			return nil, "", errPicSize
		}
		if frames := gifFrames(raw); frames == 0 || frames > maxGifFrames {
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

// gifFrames walks the blocks of a GIF without decoding any pixels and returns
// how many frames it holds, or 0 when the structure is broken. That keeps the
// memory a large animation needs out of the picture.
func gifFrames(b []byte) int {
	if len(b) < 13 || (string(b[:6]) != "GIF87a" && string(b[:6]) != "GIF89a") {
		return 0
	}
	i := 13
	if b[10]&0x80 != 0 {
		i += 3 * (1 << (uint(b[10]&7) + 1))
	}
	skipSub := func() bool {
		for i < len(b) {
			n := int(b[i])
			i++
			if n == 0 {
				return true
			}
			i += n
		}
		return false
	}
	frames := 0
	for i < len(b) {
		switch b[i] {
		case 0x3B:
			return frames
		case 0x21:
			i += 2
			if !skipSub() {
				return 0
			}
		case 0x2C:
			if i+10 > len(b) {
				return 0
			}
			flags := b[i+9]
			i += 10
			if flags&0x80 != 0 {
				i += 3 * (1 << (uint(flags&7) + 1))
			}
			i++
			if !skipSub() {
				return 0
			}
			frames++
		default:
			return 0
		}
	}
	return 0
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
	if err := a.Store.PicUploadAdd(name, userID); err != nil {
		logger.Error("PICS %v", err)
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
	r.Body = http.MaxBytesReader(w, r.Body, maxPicUpload+(1<<20))
	file, _, err := r.FormFile("file")
	if err != nil {
		a.fail(w, r, errBadImage)
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxPicUpload+1))
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
	raw, err := a.fetchStill(ctx, strings.TrimPrefix(in.File, "/"))
	if err != nil {
		logger.Error("PICS fetch still %s for uid=%d: %v", in.File, u.ID, err)
		a.fail(w, r, apiErr{http.StatusBadGateway, "err.image_invalid"})
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
		return
	}
	if err := a.Store.PicUploadForget(name); err != nil {
		logger.Error("PICS %v", err)
	}
}

// fetchStill downloads the full-size still from TMDB's CDN. It is used once, to
// make the person's own copy, so it is not cached with the artwork.
func (a *App) fetchStill(ctx context.Context, file string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Cfg.ImageBase+"/original/"+file, nil)
	if err != nil {
		return nil, err
	}
	resp, err := imgHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxStillBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxStillBytes {
		return nil, fmt.Errorf("still exceeds %d bytes", maxStillBytes)
	}
	return raw, nil
}

// apiDiscardPic deletes a picture the person uploaded and then took off the
// comment they were writing, unless a comment already shows it.
func (a *App) apiDiscardPic(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Name string }
	if err := decode(r, &in); err != nil || !ownPic(in.Name, u.ID) {
		a.fail(w, r, errBadRequest)
		return
	}
	a.dropPic(in.Name)
	a.okJSON(w, nil)
}

// SweepPics deletes uploads older than keep that no comment uses, and forgets
// the ones that are in use. Run it from a janitor so abandoned uploads do not
// pile up in the bucket.
func (a *App) SweepPics(keep time.Duration) {
	names, err := a.Store.PicUploadsBefore(store.Ts(time.Now().Add(-keep)))
	if err != nil {
		logger.Error("PICS sweep: %v", err)
		return
	}
	for _, n := range names {
		used, err := a.Store.ImageInUse(n)
		if err != nil {
			logger.Error("PICS sweep use of %s: %v", n, err)
			continue
		}
		if !used {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err := a.Media.Delete(ctx, "pics/"+n)
			cancel()
			if err != nil {
				logger.Warn("PICS sweep remove %s: %v", n, err)
				continue
			}
		}
		if err := a.Store.PicUploadForget(n); err != nil {
			logger.Error("PICS sweep %v", err)
		}
	}
}

// RunPicJanitor sweeps abandoned uploads once at start-up and then daily, until
// ctx ends.
func (a *App) RunPicJanitor(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Minute):
		a.SweepPics(picKeepFor)
	}
	tick := time.NewTicker(24 * time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			a.SweepPics(picKeepFor)
		}
	}
}

// picAbsURL is the full address of a stored picture as another server needs it.
func (a *App) picAbsURL(name string) string {
	if name == "" {
		return ""
	}
	u := a.picURL(name)
	if strings.HasPrefix(u, "/") {
		u = strings.TrimRight(a.Cfg.BaseURL, "/") + u
	}
	return u
}

// picOrigin is the scheme and host this server's pictures are loaded from.
func (a *App) picOrigin() string {
	u, err := url.Parse(a.picAbsURL("0-0000000000000000.jpg"))
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// SetFederation connects the federation service and tells it where this
// server's pictures live.
func (a *App) SetFederation(f *federation.Service) {
	a.Fed = f
	f.MediaOrigin = a.picOrigin
	f.ImageURL = a.picAbsURL
}

var (
	cspMu      sync.Mutex
	cspAt      time.Time
	cspOrigins string
)

// peerImageOrigins is the extra img-src entries for the picture hosts of the
// servers an admin has approved, so their comment pictures load in the browser.
func (a *App) peerImageOrigins() string {
	cspMu.Lock()
	defer cspMu.Unlock()
	if time.Since(cspAt) < 20*time.Second && !cspAt.IsZero() {
		return cspOrigins
	}
	list, err := a.Store.ApprovedMediaOrigins()
	if err != nil {
		logger.Error("CSP picture hosts of approved servers: %v", err)
		return cspOrigins
	}
	var b strings.Builder
	for _, o := range list {
		if cspOriginRe.MatchString(o) {
			b.WriteString(" " + o)
		}
	}
	cspOrigins, cspAt = b.String(), time.Now()
	return cspOrigins
}

func invalidateCSP() {
	cspMu.Lock()
	cspAt = time.Time{}
	cspMu.Unlock()
}

var cspOriginRe = regexp.MustCompile(`^https?://[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?$`)

// apiPicSource gives the editor the bytes of a picture the person uploaded,
// from this origin, so drawing it on a canvas does not taint the canvas.
func (a *App) apiPicSource(w http.ResponseWriter, r *http.Request) {
	u, err := a.needVerified(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	name := r.PathValue("name")
	if !ownPic(name, u.ID) {
		a.fail(w, r, errNotFound)
		return
	}
	a.serveObject(w, r, "pics/"+name)
}

// apiStillBytes gives the editor one of an episode's stills at full size,
// without storing anything: only the finished picture is ever saved.
func (a *App) apiStillBytes(w http.ResponseWriter, r *http.Request) {
	u, err := a.needVerified(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.imgLimit, "still:"+itoa(u.ID)); err != nil {
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
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	paths, err := a.episodeStills(ctx, ep)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	file := r.URL.Query().Get("file")
	listed := false
	for _, p := range paths {
		listed = listed || p == file
	}
	if !listed {
		a.fail(w, r, errBadRequest)
		return
	}
	raw, err := a.fetchStill(ctx, strings.TrimPrefix(file, "/"))
	if err != nil {
		logger.Error("PICS still bytes %s for uid=%d: %v", file, u.ID, err)
		a.fail(w, r, apiErr{http.StatusBadGateway, "err.image_invalid"})
		return
	}
	ct := http.DetectContentType(raw)
	if ct != "image/jpeg" && ct != "image/png" && ct != "image/webp" {
		a.fail(w, r, apiErr{http.StatusBadGateway, "err.image_invalid"})
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(raw); err != nil {
		logger.Warn("PICS still bytes write: %v", err)
	}
}
