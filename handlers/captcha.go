package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tvt/logger"
)

const captchaService = "https://captcha.csuit.top"

var errCaptchaUnavailable = apiErr{http.StatusServiceUnavailable, "err.captcha_unavailable"}

var captchaClient = &http.Client{Timeout: 5 * time.Second}

type captchaSpec struct {
	script  string
	verify  string
	origins []string
}

var captchaSpecs = map[string]captchaSpec{
	"cproof":    {captchaService + "/static/captcha.js", captchaService + "/api/captcha/check", []string{captchaService}},
	"turnstile": {"https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit", "https://challenges.cloudflare.com/turnstile/v0/siteverify", []string{"https://challenges.cloudflare.com"}},
	"hcaptcha":  {"https://js.hcaptcha.com/1/api.js?render=explicit", "https://api.hcaptcha.com/siteverify", []string{"https://hcaptcha.com", "https://*.hcaptcha.com"}},
	"recaptcha": {"https://www.google.com/recaptcha/api.js?render=explicit", "https://www.google.com/recaptcha/api/siteverify", []string{"https://www.google.com", "https://www.gstatic.com"}},
}

func (a *App) captchaProvider() string {
	if a.Cfg.CaptchaKey == "" {
		return ""
	}
	p := a.Cfg.CaptchaProvider
	if p == "" {
		p = "cproof"
	}
	if _, ok := captchaSpecs[p]; !ok {
		return ""
	}
	if p != "cproof" && a.Cfg.CaptchaSiteKey == "" {
		return ""
	}
	return p
}

func (a *App) captchaIncomplete() string {
	p := a.Cfg.CaptchaProvider
	if p == "" || p == "cproof" {
		return ""
	}
	if a.Cfg.CaptchaSiteKey == "" || a.Cfg.CaptchaKey == "" {
		return p
	}
	return ""
}

func (a *App) captchaOrigins() []string {
	p := a.captchaProvider()
	if p == "" {
		return nil
	}
	return captchaSpecs[p].origins
}

func (a *App) captchaCSPSources() string {
	o := a.captchaOrigins()
	if len(o) == 0 {
		return ""
	}
	return " " + strings.Join(o, " ")
}

func (a *App) captchaPageData(data map[string]any) {
	p := a.captchaProvider()
	if p == "" {
		if inc := a.captchaIncomplete(); inc != "" {
			logger.Warn("CAPTCHA provider %s is selected but its site key or secret key is missing, the contact form has no captcha", inc)
		}
		return
	}
	data["CaptchaProvider"] = p
	data["CaptchaScript"] = captchaSpecs[p].script
	data["CaptchaSite"] = a.Cfg.CaptchaSiteKey
	if p == "cproof" {
		data["CaptchaBase"] = captchaService
	}
}

func (a *App) captchaPass(ctx context.Context, token string) (bool, error) {
	p := a.captchaProvider()
	if p == "" {
		return true, nil
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	spec := captchaSpecs[p]
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	form := url.Values{"secret": {a.Cfg.CaptchaKey}, "response": {token}}
	if p == "cproof" {
		form = url.Values{"captcha_id": {token}}
	}
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, spec.verify, strings.NewReader(form.Encode()))
	if err != nil {
		logger.Error("CAPTCHA verify request failed provider=%s: %v", p, err)
		return false, errCaptchaUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if p == "cproof" {
		req.Header.Set("X-Captcha-Key", a.Cfg.CaptchaKey)
	}
	resp, err := captchaClient.Do(req)
	if err != nil {
		logger.Error("CAPTCHA provider unreachable provider=%s: %v", p, err)
		return false, errCaptchaUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Error("CAPTCHA provider rejected our request provider=%s status=%d", p, resp.StatusCode)
		return false, errCaptchaUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	if err != nil {
		logger.Error("CAPTCHA provider reply unreadable provider=%s: %v", p, err)
		return false, errCaptchaUnavailable
	}
	if p == "cproof" {
		return strings.Contains(string(body), `"solved":true`), nil
	}
	var out struct {
		Success bool     `json:"success"`
		Errors  []string `json:"error-codes"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		logger.Error("CAPTCHA provider reply invalid provider=%s: %v", p, err)
		return false, errCaptchaUnavailable
	}
	if !out.Success {
		logger.Warn("CAPTCHA token refused provider=%s codes=%v", p, out.Errors)
	}
	return out.Success, nil
}

func (a *App) contentSecurityPolicy() string {
	cp := a.captchaCSPSources()
	frame := "'none'"
	if cp != "" {
		frame = strings.TrimSpace(cp)
	}
	return "default-src 'self'; img-src 'self' data:" + a.publicMediaOrigin() + a.peerImageOrigins() + cp + "; style-src 'self'" + cp + "; script-src 'self'" + cp + "; connect-src 'self'" + cp + "; frame-src " + frame + "; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
}
