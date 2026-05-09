package feeds

import (
	"context"
	"net/http"
)

// SetValidateForTest replaces the URL validator on the Service. Tests
// outside this package use it to bypass the SSRF gate so httptest
// servers (loopback) can be subscribed to.
func (s *Service) SetValidateForTest(fn func(context.Context, string) (string, error)) {
	s.validate = fn
}

// SetPollerHTTPForTest swaps the Poller's HTTP client. Tests use this
// to bypass safehttp for httptest URLs.
func SetPollerHTTPForTest(p *Poller, c *http.Client) { p.http = c }
