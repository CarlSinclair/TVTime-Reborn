package logger

import "testing"

func TestRedactIPsToCountry(t *testing.T) {
	RememberCountry("203.0.113.9", "NL")
	RememberCountry("2001:db8::7", "BR")
	cases := map[string]string{
		"login failed from 203.0.113.9 now": "login failed from NL now",
		"peer 2001:db8:0:0:0:0:0:7 bad":     "peer BR bad",
		"unknown 198.51.100.4 here":         "unknown ?? here",
		"loopback ::1 seen":                 "loopback ?? seen",
		"fires at 12:30:45 for show 7":      "fires at 12:30:45 for show 7",
	}
	for in, want := range cases {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}
