// Package netinfo discovers the host's outbound public IP and resolves
// operator-supplied domains, so the setup wizard can tell the operator
// "your server's public IP is X — point your A record at X". The DNS
// preflight is informational only; if it disagrees with what the
// operator entered, the wizard shows hints but lets them retry rather
// than blocking the next step.
package netinfo

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nchapman/mizu/internal/safehttp"
)

// PublicIPCache caches the discovered public IP for a short window so
// the wizard can poll the DNS-check endpoint without hammering the
// upstream provider.
type PublicIPCache struct {
	mu        sync.Mutex
	value     string
	fetchedAt time.Time
	ttl       time.Duration
	client    *http.Client
	// provider is overridable in tests; production uses ipify.
	provider string
}

// NewPublicIPCache returns a cache that fetches via the safe HTTP
// client (the same SSRF-resistant Dial we use for feeds/webmentions —
// public IP services live on real public IPs, so the loopback/RFC-1918
// blocks don't affect them).
func NewPublicIPCache() *PublicIPCache {
	return &PublicIPCache{
		ttl:      5 * time.Minute,
		client:   safehttp.NewClient(),
		provider: "https://api.ipify.org",
	}
}

// Get returns the cached public IP, refreshing if older than the TTL.
// A returned error means we don't know the IP — the wizard should
// surface a "could not determine your public IP; check this manually"
// hint rather than guessing.
func (c *PublicIPCache) Get(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.value != "" && time.Since(c.fetchedAt) < c.ttl {
		return c.value, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.provider, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("public-ip probe: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("public-ip probe: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", fmt.Errorf("public-ip probe: read: %w", err)
	}
	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("public-ip probe: not an IP: %q", ip)
	}
	c.value = ip
	c.fetchedAt = time.Now()
	return ip, nil
}

// DNSResult summarises what an A/AAAA lookup of a domain returned and
// whether any of the addresses match this host's public IP. Hints are
// plain-English diagnostics the wizard renders verbatim.
type DNSResult struct {
	Domain   string   `json:"domain"`
	PublicIP string   `json:"public_ip"`
	A        []string `json:"a_records,omitempty"`
	AAAA     []string `json:"aaaa_records,omitempty"`
	Matches  bool     `json:"matches"`
	Hints    []string `json:"hints,omitempty"`
}

// LookupDomain resolves domain via the system resolver and compares
// against publicIP. The result is never an "error" the wizard cares
// about — even an NXDOMAIN response is just a hint the operator hasn't
// pointed DNS yet.
func LookupDomain(ctx context.Context, domain, publicIP string) DNSResult {
	res := DNSResult{Domain: domain, PublicIP: publicIP}
	if strings.TrimSpace(domain) == "" {
		res.Hints = append(res.Hints, "Enter a domain to check.")
		return res
	}
	resolver := &net.Resolver{}
	// A records.
	ips, err := resolver.LookupIP(ctx, "ip4", domain)
	if err != nil {
		res.Hints = append(res.Hints, fmt.Sprintf("Could not resolve A record for %s: %v", domain, err))
	} else {
		for _, ip := range ips {
			res.A = append(res.A, ip.String())
		}
	}
	// AAAA records — informational.
	if ips6, err := resolver.LookupIP(ctx, "ip6", domain); err == nil {
		for _, ip := range ips6 {
			res.AAAA = append(res.AAAA, ip.String())
		}
	}

	if publicIP == "" {
		res.Hints = append(res.Hints, "Could not detect your server's public IP. Verify this manually before enabling HTTPS.")
	} else {
		for _, a := range res.A {
			if a == publicIP {
				res.Matches = true
				break
			}
		}
		if !res.Matches && len(res.A) > 0 {
			res.Hints = append(res.Hints, fmt.Sprintf("Your A record points to %s but this server's public IP is %s. Update your DNS A record to point at %s.", strings.Join(res.A, ", "), publicIP, publicIP))
		}
		if len(res.A) == 0 && len(res.Hints) == 0 {
			res.Hints = append(res.Hints, fmt.Sprintf("No A record found for %s. Create one pointing at %s.", domain, publicIP))
		}
	}
	if len(res.AAAA) > 0 && !res.Matches {
		// IPv6 connectivity isn't required, but if a stale AAAA points
		// elsewhere it'll route some clients to the wrong host even
		// after the operator "fixes" their A record.
		res.Hints = append(res.Hints, fmt.Sprintf("An AAAA record is set (%s). If you don't intend to serve over IPv6, remove it — clients on IPv6 will reach the address it points to, not this server.", strings.Join(res.AAAA, ", ")))
	}
	return res
}

// SetProviderForTest swaps the public-IP provider URL and HTTP client.
// Test-only; production constructs the cache via NewPublicIPCache and
// never touches these fields after that.
func (c *PublicIPCache) SetProviderForTest(url string, client *http.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.provider = url
	c.client = client
	c.value = ""
	c.fetchedAt = time.Time{}
}
