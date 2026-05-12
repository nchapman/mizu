package server

import (
	"net/http"

	"github.com/unrolled/secure"
)

// SecureHeaders returns a middleware that sets the standard set of
// hardening headers. STS options are always configured; the underlying
// library only emits Strict-Transport-Security when the request itself
// arrived over TLS (r.TLS != nil), so plain-HTTP responses never carry
// the header. This means the wizard's "Enable HTTPS" flip applies to
// HSTS immediately, without restarting the process.
//
// The Content Security Policy is deliberately not set here: the admin
// SPA and sanitized feed HTML need a CSP designed around their
// specific needs and that work belongs in a dedicated change.
func SecureHeaders() func(http.Handler) http.Handler {
	s := secure.New(secure.Options{
		FrameDeny:            true,
		ContentTypeNosniff:   true,
		ReferrerPolicy:       "strict-origin-when-cross-origin",
		STSSeconds:           31536000,
		STSIncludeSubdomains: true,
		STSPreload:           false,
	})
	return s.Handler
}
