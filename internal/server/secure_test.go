package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nchapman/mizu/internal/config"
)

func TestSecureHeaders_HSTSOnlyWhenTLSEnabled(t *testing.T) {
	cases := []struct {
		name      string
		tls       config.TLS
		wantHSTS  bool
		wantFrame string
	}{
		{
			name:      "tls off omits HSTS",
			tls:       config.TLS{Enabled: false},
			wantHSTS:  false,
			wantFrame: "DENY",
		},
		{
			name:      "tls on sets HSTS",
			tls:       config.TLS{Enabled: true},
			wantHSTS:  true,
			wantFrame: "DENY",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mw := SecureHeaders(tc.tls)
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest("GET", "https://example.test/", nil)
			req.TLS = nil // unrolled/secure looks at scheme; the test URL handles it.
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			gotHSTS := w.Header().Get("Strict-Transport-Security")
			if tc.wantHSTS && gotHSTS == "" {
				t.Errorf("HSTS missing: %v", w.Header())
			}
			if !tc.wantHSTS && gotHSTS != "" {
				t.Errorf("HSTS unexpectedly set: %q", gotHSTS)
			}
			if got := w.Header().Get("X-Frame-Options"); got != tc.wantFrame {
				t.Errorf("X-Frame-Options=%q, want %q", got, tc.wantFrame)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options=%q, want nosniff", got)
			}
		})
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
