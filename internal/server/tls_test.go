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

// passthroughBase is the bare app handler buildPlainHandler falls
// through to for unknown-Host requests. The tests use a sentinel body
// to assert the fallthrough actually happened.
var passthroughBase = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("base-passthrough"))
})

func TestBuildPlainHandler_RedirectsAllowedHost(t *testing.T) {
	h := buildPlainHandler(&certmagic.Config{}, []string{"mizu.fyi"}, passthroughBase)
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

func TestBuildPlainHandler_FallsThroughForUnknownHost(t *testing.T) {
	// Attacker-controlled Host header must never be reflected into a
	// redirect target. Pre-redesign, unknown hosts got a 404; now they
	// pass through to the base handler so LAN/IP access keeps working.
	h := buildPlainHandler(&certmagic.Config{}, []string{"mizu.fyi"}, passthroughBase)
	req := httptest.NewRequest("GET", "http://evil.example/", nil)
	req.Host = "evil.example"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("code=%d, want 200 (fallthrough)", w.Code)
	}
	if got := w.Header().Get("Location"); got != "" {
		t.Errorf("unexpected Location header on fallthrough: %q", got)
	}
	if got := w.Body.String(); got != "base-passthrough" {
		t.Errorf("body=%q, want base-passthrough (proves fallthrough)", got)
	}
}

func newTestManager() *TLSManager {
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	var wg sync.WaitGroup
	m := NewTLSManager(http.NotFoundHandler(), cfg, &wg)
	// Stub the plain-handler swap so Enable's guard doesn't trip.
	m.OnPlainHandlerChange(http.NotFoundHandler(), func(http.Handler) {})
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

func TestTLSManager_PlainHandlerSwapGuard(t *testing.T) {
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	var wg sync.WaitGroup
	m := NewTLSManager(http.NotFoundHandler(), cfg, &wg)
	// Deliberately skip OnPlainHandlerChange.
	err := m.Enable(t.Context(), []string{"x.example"}, "ops@example.com", true)
	if err == nil || !strings.Contains(err.Error(), "plain handler swap not wired") {
		t.Errorf("expected plain-swap guard, got %v", err)
	}
	if s := m.Status(); s.State != "off" {
		t.Errorf("state=%q after guard-rejected Enable, want off", s.State)
	}
}

func TestTLSManager_RecordErrorAfterReadyDoesNotDowngrade(t *testing.T) {
	m := newTestManager()
	// Simulate a clean "issuing → ready" arrival from a cert_obtained
	// event, then have a listener goroutine exit with an error after
	// the fact. State should stay "ready" so the UI doesn't flap into
	// a red error banner over a working site.
	m.mu.Lock()
	m.state = "ready"
	m.mu.Unlock()
	m.recordError(errors.New("https listener wobbled"))
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

func TestTLSManager_EnableAfterShutdownRefuses(t *testing.T) {
	// A wizard click that lands after Shutdown has begun must not call
	// wg.Add: the process-wide WaitGroup is already being drained, and
	// Add-after-Wait either panics or sneaks a goroutine past shutdown.
	m := newTestManager()
	m.Shutdown(t.Context())
	err := m.Enable(t.Context(), []string{"x.example"}, "ops@example.com", true)
	if err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Errorf("expected shutting-down guard, got %v", err)
	}
	if s := m.Status(); s.State != "off" {
		t.Errorf("state=%q after post-shutdown Enable, want off", s.State)
	}
}

func TestBuildPlainHandler_StripsPortAndCase(t *testing.T) {
	h := buildPlainHandler(&certmagic.Config{}, []string{"mizu.fyi"}, passthroughBase)
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
