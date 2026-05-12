package server

import "net/http"

// HSTS returns a middleware that emits Strict-Transport-Security on
// HTTPS responses, but only after `realCert()` returns true. The intent
// is to never pin a self-signed cert in browsers: if HSTS were sent
// while the listener is serving the bootstrap self-signed cert, a
// future cert wipe (DB reset, fresh disk, regenerated self-signed)
// would leave operators unable to recover — browsers honoring STS
// refuse the click-through escape hatch on cert errors.
//
// Once CertMagic has obtained a real cert, realCert flips true and HSTS
// kicks in. Plain-HTTP responses (the redirect listener) never get the
// header regardless, because r.TLS is nil there.
//
// Header value matches the previous one-year + includeSubdomains
// posture; preload is left off intentionally (it's a one-way commitment
// the operator should opt into separately).
func HSTS(realCert func() bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.TLS != nil && realCert != nil && realCert() {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}
