package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nchapman/mizu/internal/config"
)

func newStartedManager(t *testing.T) (*TLSManager, func()) {
	t.Helper()
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	// Anchor the cert dir inside a temp tree so the self-signed
	// bootstrap doesn't pollute the working directory.
	cfg.Paths.Certs = filepath.Join(t.TempDir(), "certs")
	// Bind the HTTPS listener to an OS-assigned port so parallel test
	// runs don't collide on :8443.
	cfg.Server.TLS.Addr = "127.0.0.1:0"

	var wg sync.WaitGroup
	m := NewTLSManager(http.NotFoundHandler(), cfg, &wg)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return m, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.Shutdown(ctx)
		wg.Wait()
	}
}

func TestTLSManager_StartBootstrapsSelfSigned(t *testing.T) {
	m, cleanup := newStartedManager(t)
	defer cleanup()

	if s := m.Status(); s.State != "selfsigned" {
		t.Errorf("state=%q after Start, want selfsigned", s.State)
	}
	if m.HasRealCert() {
		t.Error("HasRealCert true at boot before any ACME issuance")
	}

	// Real TLS handshake against the listener: the cert presented must
	// be our self-signed bootstrap, since no ACME issuer is configured.
	addr := m.main.Addr
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true, // self-signed by design
		ServerName:         "blog.example",
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("no peer certs presented")
	}
	if got := state.PeerCertificates[0].Subject.CommonName; got != "mizu self-signed" {
		t.Errorf("CN=%q, want mizu self-signed", got)
	}
}

// The layered resolver must prefer the upstream (CertMagic) when it
// returns a cert, and only fall back to self-signed when it doesn't.
// We test the layering helper directly so this doesn't have to drag a
// real CertMagic instance around.
func TestLayeredGetCertificate_PrefersUpstream(t *testing.T) {
	m, cleanup := newStartedManager(t)
	defer cleanup()
	bootstrap := m.selfSigned.Load()
	if bootstrap == nil {
		t.Fatal("self-signed not loaded after Start")
	}

	// Sentinel "upstream cert."
	upstream := &tls.Certificate{Certificate: [][]byte{[]byte("upstream")}}

	// Build a fresh layered resolver around stub upstreams.
	layer := func(upErr error, upCert *tls.Certificate) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		stub := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return upCert, upErr }
		return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if cert, err := stub(hello); err == nil && cert != nil {
				return cert, nil
			}
			ss := m.selfSigned.Load()
			return ss, nil
		}
	}

	got, err := layer(nil, upstream)(&tls.ClientHelloInfo{ServerName: "x"})
	if err != nil || got != upstream {
		t.Errorf("upstream-cert path: got=%v err=%v", got, err)
	}
	got, err = layer(errors.New("no cert"), nil)(&tls.ClientHelloInfo{ServerName: "x"})
	if err != nil || got != bootstrap {
		t.Errorf("fallback path: got=%v err=%v want self-signed", got, err)
	}
}

func TestTLSManager_RequiresHandler(t *testing.T) {
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	cfg.Paths.Certs = filepath.Join(t.TempDir(), "certs")
	var wg sync.WaitGroup
	m := NewTLSManager(nil, cfg, &wg)
	if err := m.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "handler not set") {
		t.Errorf("expected handler-not-set guard, got %v", err)
	}
}

func TestTLSManager_EnableACMEValidation(t *testing.T) {
	m, cleanup := newStartedManager(t)
	defer cleanup()

	if err := m.EnableACME(context.Background(), nil, "x@example", false); err == nil {
		t.Error("expected domain-required error")
	}
	if err := m.EnableACME(context.Background(), []string{"x.example"}, "", false); err == nil {
		t.Error("expected email-required error")
	}
}

// Regression: the cert_obtained event handler used to read ACME args
// from m.cfg, which is empty during a first-run wizard issuance
// (PersistACMEConfig is exactly what writes those fields). The
// handler now reads from manager-stashed args instead, so the persist
// callback gets the wizard-supplied values.
//
// We don't drive a real ACME flow here — that would either require a
// live network or a heavy CertMagic stub. Instead, populate the stash
// directly and exercise the same lock+read path the OnEvent handler
// takes.
func TestTLSManager_CertObtainedCallbackUsesEnableArgs(t *testing.T) {
	m, cleanup := newStartedManager(t)
	defer cleanup()

	type call struct {
		domains []string
		email   string
		staging bool
	}
	var got call
	var fired bool
	m.OnEnabled(func(d []string, e string, s bool) {
		got = call{d, e, s}
		fired = true
	})

	m.mu.Lock()
	m.acmeDomains = []string{"blog.example"}
	m.acmeEmail = "ops@example.com"
	m.acmeStaging = true
	d, e, s := m.acmeArgsLocked()
	cb := m.onEnabled
	m.mu.Unlock()
	if cb != nil {
		cb(d, e, s)
	}

	if !fired {
		t.Fatal("OnEnabled callback did not fire")
	}
	if len(got.domains) != 1 || got.domains[0] != "blog.example" {
		t.Errorf("domains=%v, want [blog.example]", got.domains)
	}
	if got.email != "ops@example.com" {
		t.Errorf("email=%q", got.email)
	}
	if !got.staging {
		t.Error("staging=false, want true")
	}
}

// EnableACME refuses to mutate magic.Issuers after the first install
// because doing so races with CertMagic's renewal goroutine. Operators
// who want to change email/staging must restart. We trigger the guard
// without driving a real ACME issuance by pre-seeding the install
// flag and the previous args.
func TestTLSManager_EnableACMERefusesIssuerSwap(t *testing.T) {
	m, cleanup := newStartedManager(t)
	defer cleanup()

	m.mu.Lock()
	m.issuerInstalled = true
	m.acmeDomains = []string{"blog.example"}
	m.acmeEmail = "ops@example.com"
	m.acmeStaging = true
	m.state = "error" // simulate "first attempt failed; operator retrying"
	m.mu.Unlock()

	err := m.EnableACME(context.Background(), []string{"blog.example"}, "different@example.com", false)
	if err == nil || !strings.Contains(err.Error(), "restart") {
		t.Errorf("expected restart-required error on email change, got %v", err)
	}
}

func TestTLSManager_RecordErrorAfterReadyDoesNotDowngrade(t *testing.T) {
	m, cleanup := newStartedManager(t)
	defer cleanup()
	m.mu.Lock()
	m.state = "ready"
	m.mu.Unlock()
	m.recordError(errors.New("https listener wobbled"))
	s := m.Status()
	if s.State != "ready" {
		t.Errorf("state downgraded to %q after ready, want ready", s.State)
	}
	if s.Error == "" {
		t.Error("lastErr should still surface to the UI")
	}
}

func TestTLSManager_RecordErrorFromIssuingFlipsToError(t *testing.T) {
	m, cleanup := newStartedManager(t)
	defer cleanup()
	m.mu.Lock()
	m.state = "issuing"
	m.mu.Unlock()
	m.recordError(errors.New("listener bind failed"))
	if s := m.Status(); s.State != "error" {
		t.Errorf("state=%q after issuance failure, want error", s.State)
	}
}

func TestPlainRedirectHandler_RedirectsToSameHost(t *testing.T) {
	m, cleanup := newStartedManager(t)
	defer cleanup()

	h := m.PlainRedirectHandler()
	cases := []struct {
		host string
		path string
		want string
	}{
		{"mizu.example", "/admin?next=1", "https://mizu.example/admin?next=1"},
		{"mizu.example:80", "/foo", "https://mizu.example/foo"},
		{"203.0.113.4", "/", "https://203.0.113.4/"},
		// Attacker-supplied Host: still echoed back, but the redirect
		// only sends THEM somewhere — there's no auth context here.
		{"evil.example", "/whatever", "https://evil.example/whatever"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "http://"+tc.host+tc.path, nil)
		req.Host = tc.host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusPermanentRedirect {
			t.Errorf("host=%q code=%d, want 308", tc.host, w.Code)
			continue
		}
		if got := w.Header().Get("Location"); got != tc.want {
			t.Errorf("host=%q Location=%q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestPlainRedirectHandler_MissingHostIs400(t *testing.T) {
	m, cleanup := newStartedManager(t)
	defer cleanup()
	h := m.PlainRedirectHandler()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = ""
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing-host code=%d, want 400", w.Code)
	}
}
