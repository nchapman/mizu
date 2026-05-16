package feeds

import (
	"context"
	"net/http"
	"time"
)

// SetNowForTest overrides the Store's clock. Tests use this to make
// timestamp assertions deterministic without sleeping.
func (s *Store) SetNowForTest(fn func() time.Time) { s.now = fn }

// SetValidateForTest replaces the URL validator on the Service. Tests
// outside this package use it to bypass the SSRF gate so httptest
// servers (loopback) can be subscribed to.
func (s *Service) SetValidateForTest(fn func(context.Context, string) (string, error)) {
	s.validate = fn
}

// SetPollerHTTPForTest swaps the Poller's HTTP client. Tests use this
// to bypass safehttp for httptest URLs.
func SetPollerHTTPForTest(p *Poller, c *http.Client) { p.http = c }

// SetDiscoverHTTPForTest swaps the Service's discovery HTTP client.
// Tests use this to point Discover at loopback httptest servers.
func (s *Service) SetDiscoverHTTPForTest(c *http.Client) { s.discoverHTTP = c }

// SetDiscoverForTest replaces the Service's feed-discovery function so
// tests can short-circuit the network fetch entirely (e.g. when
// subscribing to URLs whose hosts aren't reachable).
func (s *Service) SetDiscoverForTest(fn func(context.Context, *http.Client, string) (string, error)) {
	s.discover = fn
}
