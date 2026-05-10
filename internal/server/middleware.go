// Package server holds the cross-cutting HTTP plumbing that turns a
// vanilla net/http stack into something safe to expose to the open
// internet: TLS via CertMagic, security headers, and rate limiting.
//
// The package is deliberately thin. It wraps battle-tested upstreams
// (caddyserver/certmagic, go-chi/httprate, unrolled/secure) so we
// don't reinvent any wheels. Each helper is small enough to read in a
// single sitting; the value is in the choice of dependencies, not in
// the glue.
package server

import (
	"net/http"

	"github.com/go-chi/httprate"

	"github.com/nchapman/mizu/internal/config"
)

// RateLimit returns a chi-compatible middleware that enforces the
// given per-IP request budget. A zero or negative Requests value
// disables the limiter (returns the handler unchanged), so a misset
// config can never wedge a route closed.
func RateLimit(spec config.RateSpec) func(http.Handler) http.Handler {
	if spec.Requests <= 0 || spec.Per <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return httprate.LimitByIP(spec.Requests, spec.Per)
}
