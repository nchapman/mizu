// Package safehttp provides an HTTP client that refuses to dial
// private, loopback, link-local, or otherwise non-routable addresses.
// Use it for any request whose URL came from outside the operator —
// feed URLs, webmention sources, OEmbed lookups, etc. — to neutralize
// SSRF.
//
// Limitation: DNS rebinding can still race between the resolution we
// do here and a later one inside the kernel. For a single-user
// deployment that's acceptable; a hardened version would carry the
// resolved IP through to the Dial call's address itself (which we do
// here for the chosen IP, but a malicious resolver could still serve
// different IPs across the verification and connect calls).
package safehttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// allowPrivateHostsEnv is a development-only escape hatch. When set to
// "1", "true", or "yes", IsBlockedIP returns false for everything, so a
// dev docker-compose with two mizu instances on a private bridge
// network can actually exchange feeds and webmentions. Never set this
// in production — it disables the only line of defense against SSRF
// against the host's metadata service, internal admin panels, etc.
const allowPrivateHostsEnv = "MIZU_ALLOW_PRIVATE_HOSTS"

func allowPrivateHosts() bool {
	switch os.Getenv(allowPrivateHostsEnv) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// Default timeouts. Generous enough for slow-but-legitimate sites,
// tight enough to bound a stuck request.
const (
	defaultDialTimeout    = 10 * time.Second
	defaultRequestTimeout = 30 * time.Second
)

// NewClient returns an *http.Client that blocks connections to
// non-routable addresses at dial time.
func NewClient() *http.Client {
	dialer := &net.Dialer{Timeout: defaultDialTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("no addresses for %s", host)
			}
			for _, ip := range ips {
				if IsBlockedIP(ip) {
					return nil, fmt.Errorf("blocked address %s for host %s", ip, host)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
	return &http.Client{
		Timeout:   defaultRequestTimeout,
		Transport: transport,
	}
}

// IsBlockedIP returns true for ranges we never want to fetch from:
// loopback, link-local (incl. cloud metadata at 169.254.169.254),
// private RFC-1918, ULA, multicast, and unspecified.
func IsBlockedIP(ip net.IP) bool {
	if allowPrivateHosts() {
		return false
	}
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}
