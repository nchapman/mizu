package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nchapman/mizu/internal/config"
)

// HSTS must follow the actual transport of the request, not a static
// config flag captured at startup. A plain-HTTP request never gets it
// (browsers honoring STS over plain HTTP would brick the site); a TLS
// request always does, even if cfg.Server.TLS.Enabled was false when
// the middleware was built — that's the wizard-flip case.
func TestSecureHeaders_HSTSGatedByRequestTLS(t *testing.T) {
	mw := SecureHeaders()
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("plain http omits HSTS", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.test/", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if got := w.Header().Get("Strict-Transport-Security"); got != "" {
			t.Errorf("HSTS unexpectedly set on plain HTTP: %q", got)
		}
		if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("X-Frame-Options=%q, want DENY", got)
		}
		if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options=%q, want nosniff", got)
		}
	})

	t.Run("tls request emits HSTS", func(t *testing.T) {
		req := httptest.NewRequest("GET", "https://example.test/", nil)
		req.TLS = &tls.ConnectionState{}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if got := w.Header().Get("Strict-Transport-Security"); got == "" {
			t.Errorf("HSTS missing on TLS request: %v", w.Header())
		}
	})
}

func TestRateLimit_DisabledOnZeroSpec(t *testing.T) {
	mw := RateLimit(config.RateSpec{Requests: 0, Per: 0})
	called := 0
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 100; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("disabled limiter blocked request: code=%d", w.Code)
		}
	}
	if called != 100 {
		t.Errorf("calls=%d, want 100", called)
	}
}
