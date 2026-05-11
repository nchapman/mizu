package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/caddyserver/certmagic"

	"github.com/nchapman/mizu/internal/config"
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

func newTestManager() *TLSManager {
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	var wg sync.WaitGroup
	// Stub the handler so Enable's nil-handler guard doesn't trip.
	m := NewTLSManager(http.NotFoundHandler(), cfg, &wg)
	return m
}

func TestTLSManager_NilHandlerGuard(t *testing.T) {
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	var wg sync.WaitGroup
	m := NewTLSManager(nil, cfg, &wg)
	err := m.Enable(t.Context(), []string{"x.example"}, "ops@example.com", true)
	if err == nil || !strings.Contains(err.Error(), "handler not set") {
		t.Errorf("expected handler-not-set guard, got %v", err)
	}
	if s := m.Status(); s.State != "off" {
		t.Errorf("state=%q after guard-rejected Enable, want off", s.State)
	}
}

func TestTLSManager_EnableRejectsPortConflict(t *testing.T) {
	m := newTestManager()
	// Force the plain HTTP listener to overlap the default :443 — the
	// guard inside Enable should refuse rather than letting the
	// listener goroutine die mid-bind.
	m.cfg.Server.Addr = ":443"
	err := m.Enable(t.Context(), []string{"x.example"}, "ops@example.com", true)
	if err == nil || !strings.Contains(err.Error(), "overlaps the TLS port") {
		t.Errorf("expected port-conflict guard, got %v", err)
	}
	if s := m.Status(); s.State != "error" {
		t.Errorf("state=%q after conflict, want error", s.State)
	}
}

func TestTLSManager_RecordErrorAfterReadyDoesNotDowngrade(t *testing.T) {
	m := newTestManager()
	// Simulate a clean "issuing → ready" arrival from a cert_obtained
	// event, then have a listener goroutine exit with an error after
	// the fact (e.g. port 80 wobble). State should stay "ready" so the
	// UI doesn't flap into a red error banner over a working site.
	m.mu.Lock()
	m.state = "ready"
	m.mu.Unlock()
	m.recordError(errors.New("redirect listener wobbled"))
	s := m.Status()
	if s.State != "ready" {
		t.Errorf("state downgraded to %q after ready, want ready", s.State)
	}
	if s.Error == "" {
		t.Error("lastErr should still surface to the UI for diagnostics")
	}
}

func TestTLSManager_RecordErrorFromIssuingFlipsToError(t *testing.T) {
	m := newTestManager()
	m.mu.Lock()
	m.state = "issuing"
	m.mu.Unlock()
	m.recordError(errors.New("listener bind failed"))
	if s := m.Status(); s.State != "error" {
		t.Errorf("state=%q after issuance failure, want error", s.State)
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
