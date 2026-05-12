package server

import (
	"net/http"

	"github.com/unrolled/secure"
)

// SecureHeaders returns a middleware that sets the standard set of
// hardening headers: deny framing, sniffing, and constrain referrer
// leakage. HSTS is intentionally NOT here — see the HSTS middleware,
// which gates emission on a real (non-self-signed) cert being available.
//
// The Content Security Policy is deliberately not set here either: the
// admin SPA and sanitized feed HTML need a CSP designed around their
// specific needs and that work belongs in a dedicated change.
func SecureHeaders() func(http.Handler) http.Handler {
	s := secure.New(secure.Options{
		FrameDeny:          true,
		ContentTypeNosniff: true,
		ReferrerPolicy:     "strict-origin-when-cross-origin",
	})
	return s.Handler
}
