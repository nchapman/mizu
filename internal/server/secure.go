package server

import (
	"net/http"

	"github.com/unrolled/secure"

	"github.com/nchapman/mizu/internal/config"
)

// SecureHeaders returns a middleware that sets the standard set of
// hardening headers. HSTS is intentionally only enabled when TLS is
// on — emitting Strict-Transport-Security on a plain-HTTP origin
// would brick the site in browsers that honored it.
//
// The Content Security Policy is deliberately not set here: the admin
// SPA and sanitized feed HTML need a CSP designed around their
// specific needs and that work belongs in a dedicated change.
func SecureHeaders(tls config.TLS) func(http.Handler) http.Handler {
	opts := secure.Options{
		FrameDeny:          true,
		ContentTypeNosniff: true,
		ReferrerPolicy:     "strict-origin-when-cross-origin",
	}
	if tls.Enabled {
		opts.SSLRedirect = false // CertMagic's port-80 handler does this.
		opts.STSSeconds = 31536000
		opts.STSIncludeSubdomains = true
		opts.STSPreload = false
	}
	s := secure.New(opts)
	return s.Handler
}
