package federation

import (
	"net"
	"testing"
)

func TestValidHost(t *testing.T) {
	for _, h := range []string{"tvt.example.com", "a.b.c.example.org:8443", "x1.example.io"} {
		if !ValidHost(h, false) {
			t.Errorf("%q should be a valid host", h)
		}
	}
	for _, h := range []string{"", "localhost", "127.0.0.1", "10.0.0.1:80", "evil.com/path", "evil.com@other.com", "a b.com", "exa_mple.com", "-bad.example.com", "http://x.example.com", "x..example.com"} {
		if ValidHost(h, false) {
			t.Errorf("%q must not be accepted", h)
		}
	}
	if !ValidHost("127.0.0.1:9000", true) {
		t.Errorf("insecure mode (development) allows local addresses")
	}
}

func TestPublicIP(t *testing.T) {
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "2606:4700::1111"} {
		if !publicIP(net.ParseIP(ip)) {
			t.Errorf("%s is public", ip)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "::1", "fe80::1", "fd00::1", "0.0.0.0", "224.0.0.1"} {
		if publicIP(net.ParseIP(ip)) {
			t.Errorf("%s must be blocked", ip)
		}
	}
}
