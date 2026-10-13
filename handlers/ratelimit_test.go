package handlers_test

import (
	"net/http"
	"strings"
	"testing"
)

func loginAttempt(t *testing.T, base, ip, country string) int {
	c := newClient(t, base)
	req, err := http.NewRequest("POST", base+"/api/auth/login", strings.NewReader(`{"email":"nobody@example.com","password":"wrong-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrf())
	req.Header.Set("X-Forwarded-For", ip)
	req.Header.Set("CF-IPCountry", country)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestRateLimitIsPerAddressNotPerCountry(t *testing.T) {
	e := newEnv(t)
	limited := false
	for i := 0; i < 40; i++ {
		if loginAttempt(t, e.srv.URL, "203.0.113.10", "NL") == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("one address was never rate limited")
	}
	if code := loginAttempt(t, e.srv.URL, "203.0.113.77", "NL"); code == http.StatusTooManyRequests {
		t.Fatalf("a different address in the same country shares the limit")
	}
}
