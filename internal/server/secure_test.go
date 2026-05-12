package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nchapman/mizu/internal/config"
)

// SecureHeaders no longer touches HSTS — that's HSTS()'s job. This
// just confirms the static hardening headers still land on every
// response.
func TestSecureHeaders_StaticHeaders(t *testing.T) {
	mw := SecureHeaders()
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "https://example.test/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options=%q, want DENY", got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options=%q, want nosniff", got)
	}
	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS leaked from SecureHeaders: %q (should come from HSTS() instead)", got)
	}
}

// HSTS is gated by BOTH the request being TLS AND the realCert
// predicate returning true. Either condition false ⇒ no header. This
// keeps self-signed boot from pinning the bootstrap cert in browsers.
func TestHSTS_Gating(t *testing.T) {
	cases := []struct {
		name     string
		realCert bool
		tlsState *tls.ConnectionState
		want     string
	}{
		{"plain http no header", false, nil, ""},
		{"plain http with cert ready still no header", true, nil, ""},
		{"tls but self-signed (predicate false) no header", false, &tls.ConnectionState{}, ""},
		{"tls with real cert sets header", true, &tls.ConnectionState{}, "max-age=31536000; includeSubDomains"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mw := HSTS(func() bool { return tc.realCert })
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest("GET", "https://example.test/", nil)
			req.TLS = tc.tlsState
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if got := w.Header().Get("Strict-Transport-Security"); got != tc.want {
				t.Errorf("STS=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestHSTS_NilPredicateIsSafe(t *testing.T) {
	mw := HSTS(nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "https://example.test/", nil)
	req.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS emitted with nil predicate: %q", got)
	}
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
