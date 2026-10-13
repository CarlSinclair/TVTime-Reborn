// Package captcha is the rotate-the-image challenge used by CShop, CTime and
// CBots, with its proof-of-work and per-address risk scoring.
package captcha

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"
	"tvt/assets"
	"tvt/logger"
)

type captchaState struct {
	ImageIndex    int
	TargetAngle   int
	Attempts      int
	Solved        bool
	Expires       time.Time
	CSRFToken     string
	PoWSalt       string
	PoWDifficulty int
	VisualReq     bool
}

type ipStats struct {
	Score    int
	LastSeen time.Time
}

var (
	captchaStore = make(map[string]*captchaState)
	captchaMu    sync.RWMutex

	ipStore = make(map[string]*ipStats)
	ipMu    sync.Mutex

	captchaImages = []string{
		"static/img/captcha/1.png", "static/img/captcha/2.png", "static/img/captcha/3.png",
		"static/img/captcha/4.png", "static/img/captcha/5.png", "static/img/captcha/6.png",
		"static/img/captcha/7.png", "static/img/captcha/8.png", "static/img/captcha/9.png",
	}
)

const captchaWindow = 5 * time.Minute
const captchaMaxAttempts = 8

// assessRisk determines the PoW difficulty and whether a visual challenge is required.
func assessRisk(ip string) (difficulty int, visualRequired bool) {
	ipMu.Lock()
	defer ipMu.Unlock()

	now := time.Now()
	stat, exists := ipStore[ip]
	if !exists {
		stat = &ipStats{Score: 0, LastSeen: now}
		ipStore[ip] = stat
	}

	// Decay score by 1 point every 30 seconds of inactivity
	decay := int(now.Sub(stat.LastSeen).Seconds() / 60)
	if decay > 0 {
		stat.Score -= decay
		if stat.Score < 0 {
			stat.Score = 0
		}
	}
	stat.LastSeen = now

	difficulty = 3
	visualRequired = false

	// Trigger visual slider on the 3rd rapid request (Score >= 2)
	// This ensures it appears BEFORE the 5-request RateLimitMiddleware fires a 429
	if stat.Score >= 2 {
		visualRequired = true
	}
	if stat.Score >= 6 {
		difficulty = 4
	}
	if stat.Score >= 12 {
		difficulty = 5
	}

	stat.Score += 1 // Increment score per request

	return difficulty, visualRequired
}

func recordCaptchaResult(ip string, success bool, wasVisual bool) {
	ipMu.Lock()
	defer ipMu.Unlock()
	stat, exists := ipStore[ip]
	if !exists {
		return
	}
	if success {
		// Only reward actual visual solutions. Invisible PoW autosolves are neutral to prevent farming.
		if wasVisual {
			stat.Score -= 5
			if stat.Score < 0 {
				stat.Score = 0
			}
		}
	} else {
		stat.Score += 3 // Penalize failures heavily
	}
}

func randInt(max int) int {
	if max <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return int(n.Int64())
}

func rotateNearest(src image.Image, degrees int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.SetRGBA(x, y, color.RGBA{0, 0, 0, 0})
		}
	}

	theta := float64(degrees) * (math.Pi / 180.0)
	cosT := math.Cos(theta)
	sinT := math.Sin(theta)

	cx := float64(w-1) / 2.0
	cy := float64(h-1) / 2.0

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := float64(x) - cx
			dy := float64(y) - cy

			sx := (dx*cosT + dy*sinT) + cx
			sy := (-dx*sinT + dy*cosT) + cy

			ix := int(math.Round(sx))
			iy := int(math.Round(sy))

			if ix >= 0 && ix < w && iy >= 0 && iy < h {
				dst.Set(x, y, src.At(b.Min.X+ix, b.Min.Y+iy))
			}
		}
	}

	return dst
}

func Generate(csrfToken, ip string) (string, string, int, error) {
	if len(captchaImages) == 0 {
		return "", "", 0, fmt.Errorf("no captcha images configured")
	}

	difficulty, visualReq := assessRisk(ip)

	imgIdx := randInt(len(captchaImages))
	startAngle := randInt(360)

	if startAngle >= 0 && startAngle <= 12 {
		startAngle = 90
	}
	if startAngle >= 348 && startAngle <= 359 {
		startAngle = 270
	}

	idBytes := make([]byte, 12)
	_, _ = rand.Read(idBytes)
	realID := hex.EncodeToString(idBytes)

	saltBytes := make([]byte, 6)
	_, _ = rand.Read(saltBytes)
	salt := hex.EncodeToString(saltBytes)

	imgData, err := fs.ReadFile(assets.FS, captchaImages[imgIdx])
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to load captcha image: %w", err)
	}

	srcImg, err := png.Decode(bytes.NewReader(imgData))
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to decode captcha png: %w", err)
	}

	rotated := rotateNearest(srcImg, startAngle)
	var out bytes.Buffer
	if err := png.Encode(&out, rotated); err != nil {
		return "", "", 0, fmt.Errorf("failed to encode rotated captcha png: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(out.Bytes())
	uri := fmt.Sprintf("data:image/png;base64,%s", encoded)

	target := (360 - (startAngle % 360)) % 360

	purgeExpired()
	captchaMu.Lock()
	captchaStore[realID] = &captchaState{
		ImageIndex:    imgIdx,
		TargetAngle:   target,
		Attempts:      0,
		Solved:        false,
		Expires:       time.Now().Add(captchaWindow),
		CSRFToken:     csrfToken,
		PoWSalt:       salt,
		PoWDifficulty: difficulty,
		VisualReq:     visualReq,
	}
	captchaMu.Unlock()

	visFlag := "0"
	if visualReq {
		visFlag = "1"
	}

	// Format: realID_salt_difficulty_visualReq
	compositeID := fmt.Sprintf("%s_%s_%d_%s", realID, salt, difficulty, visFlag)

	return compositeID, uri, 0, nil
}

// Verify checks a solve attempt. committed distinguishes a real,
// user-released guess from the continuous live-preview checks the
// drag-to-rotate widget fires while the user is still adjusting (see
// captcha.js). Only committed guesses count against the attempt cap and the
// IP risk score - otherwise a single careful drag gesture, which pauses and
// re-checks many times before landing on the right angle, exhausts the cap
// and gets misreported to the user as the captcha having expired.
func Verify(compositeID, solution, csrfToken, powNonce, ip string, committed bool) (bool, bool) {
	parts := strings.Split(compositeID, "_")
	realID := parts[0]

	captchaMu.Lock()
	state, exists := captchaStore[realID]
	captchaMu.Unlock()

	if !exists || time.Now().After(state.Expires) {
		if exists {
			captchaMu.Lock()
			delete(captchaStore, realID)
			captchaMu.Unlock()
		}
		return false, true
	}

	// The session token the challenge was issued for must be the one presenting the answer.
	if state.CSRFToken != "" && csrfToken != "" && subtle.ConstantTimeCompare([]byte(state.CSRFToken), []byte(csrfToken)) != 1 {
		logger.Warn("CAPTCHA FAILURE [ID %s]: CSRF mismatch.", realID)
		recordCaptchaResult(ip, false, state.VisualReq)
		return false, true
	}

	if state.Solved {
		return true, false
	}

	// 1. Verify Proof of Work
	hashInput := fmt.Sprintf("%s%s", state.PoWSalt, powNonce)
	hashBytes := sha256.Sum256([]byte(hashInput))
	hashHex := hex.EncodeToString(hashBytes[:])
	requiredPrefix := strings.Repeat("0", state.PoWDifficulty)

	if !strings.HasPrefix(hashHex, requiredPrefix) {
		if !committed {
			return false, false
		}
		recordCaptchaResult(ip, false, state.VisualReq)

		captchaMu.Lock()
		state.Attempts++
		if state.Attempts >= captchaMaxAttempts {
			delete(captchaStore, realID)
			captchaMu.Unlock()
			return false, true
		}
		captchaMu.Unlock()
		return false, false
	}

	// 2. Verify Visual Angle (if required)
	if state.VisualReq {
		userAngle, err := strconv.Atoi(solution)
		if err != nil {
			recordCaptchaResult(ip, false, true)
			return false, false
		}
		userAngle = (userAngle%360 + 360) % 360

		diff := math.Abs(float64(userAngle - state.TargetAngle))
		if diff > 180 {
			diff = 360 - diff
		}

		if diff > 10 {
			if !committed {
				return false, false
			}
			recordCaptchaResult(ip, false, true)

			captchaMu.Lock()
			state.Attempts++
			if state.Attempts >= captchaMaxAttempts {
				delete(captchaStore, realID)
				captchaMu.Unlock()
				return false, true
			}
			captchaMu.Unlock()
			return false, false
		}
	}

	// Success!
	captchaMu.Lock()
	state.Solved = true
	state.Expires = time.Now().Add(captchaWindow)
	captchaMu.Unlock()

	recordCaptchaResult(ip, true, state.VisualReq)
	return true, false
}

// IsSolved is the last gate before a login/checkout/reset submission
// actually goes through, so it consumes the captcha on a successful check -
// without this, a solved captcha stayed valid (and replayable) for the rest
// of its 5-minute window, letting one solve cover unlimited submission
// attempts and defeating the point of requiring it at all.
func IsSolved(compositeID string) bool {
	parts := strings.Split(compositeID, "_")
	realID := parts[0]

	captchaMu.Lock()
	defer captchaMu.Unlock()

	state, ok := captchaStore[realID]
	if !ok {
		return false
	}
	if time.Now().After(state.Expires) {
		delete(captchaStore, realID)
		return false
	}
	if state.Solved {
		delete(captchaStore, realID)
		return true
	}
	return false
}

// purgeExpired drops challenges that were never answered, so the store cannot
// grow without bound.
func purgeExpired() {
	now := time.Now()
	captchaMu.Lock()
	for id, st := range captchaStore {
		if now.After(st.Expires) {
			delete(captchaStore, id)
		}
	}
	captchaMu.Unlock()
	ipMu.Lock()
	for ip, st := range ipStore {
		if now.Sub(st.LastSeen) > time.Hour {
			delete(ipStore, ip)
		}
	}
	ipMu.Unlock()
}

// ResetRisk forgets every address's score and every open challenge. Tests use it
// so one test's requests do not make the next one's challenge harder.
func ResetRisk() {
	ipMu.Lock()
	ipStore = make(map[string]*ipStats)
	ipMu.Unlock()
	captchaMu.Lock()
	captchaStore = make(map[string]*captchaState)
	captchaMu.Unlock()
}
