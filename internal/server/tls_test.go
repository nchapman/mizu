package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/certmagic"
)

func TestBuildHTTPHandler_RedirectsAllowedHost(t *testing.T) {
	h := buildHTTPHandler(&certmagic.Config{}, []string{"mizu.fyi"})
	req := httptest.NewRequest("GET", "http://mizu.fyi/notes/abc?x=1", nil)
	req.Host = "mizu.fyi"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusPermanentRedirect {
		t.Fatalf("code=%d, want 308", w.Code)
	}
	if got := w.Header().Get("Location"); got != "https://mizu.fyi/notes/abc?x=1" {
		t.Errorf("Location=%q", got)
	}
}

func TestBuildHTTPHandler_RejectsUnknownHost(t *testing.T) {
	h := buildHTTPHandler(&certmagic.Config{}, []string{"mizu.fyi"})
	// Attacker-controlled Host header — must not be reflected into
	// the redirect target.
	req := httptest.NewRequest("GET", "http://evil.example/", nil)
	req.Host = "evil.example"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("code=%d, want 404", w.Code)
	}
	if got := w.Header().Get("Location"); got != "" {
		t.Errorf("unexpected Location header on rejection: %q", got)
	}
}

func TestBuildHTTPHandler_StripsPortAndCase(t *testing.T) {
	h := buildHTTPHandler(&certmagic.Config{}, []string{"mizu.fyi"})
	req := httptest.NewRequest("GET", "http://Mizu.FYI:8080/x", nil)
	req.Host = "Mizu.FYI:8080"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusPermanentRedirect {
		t.Fatalf("code=%d, want 308 for case-insensitive match", w.Code)
	}
	if got := w.Header().Get("Location"); got != "https://mizu.fyi/x" {
		t.Errorf("Location=%q", got)
	}
}
