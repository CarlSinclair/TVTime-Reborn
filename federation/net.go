package federation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)

var (
	ErrBadHost     = errors.New("federation: invalid host")
	ErrUnreachable = errors.New("federation: server unreachable")
)

// ValidHost accepts a bare host name with an optional port. Without insecure
// mode it must be a dotted name, never a bare label or an IP literal.
func ValidHost(h string, insecure bool) bool {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" || len(h) > 253 || !hostRe.MatchString(h) {
		return false
	}
	if insecure {
		return true
	}
	name := h
	if i := strings.LastIndex(h, ":"); i >= 0 {
		name = h[:i]
	}
	if !strings.Contains(name, ".") || net.ParseIP(name) != nil {
		return false
	}
	return true
}

var blockedNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8"} {
		_, n, err := net.ParseCIDR(c)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}()

func publicIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// newClient makes the HTTP client used to reach other servers. It refuses to
// connect to private, loopback or link-local addresses (checked after DNS, at
// dial time) and never follows redirects, so a hostile server cannot aim this
// one at its own network.
func newClient(insecure bool) *http.Client {
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if insecure {
				return dialer.DialContext(ctx, network, addr)
			}
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if !publicIP(ip.IP) {
					return nil, fmt.Errorf("federation: %s resolves to a non-public address", host)
				}
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("federation: %s did not resolve", host)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		MaxIdleConns:          20,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

const maxBody = 64 << 10

func readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBody {
		return nil, errors.New("federation: body too large")
	}
	return b, nil
}
