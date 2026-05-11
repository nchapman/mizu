package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/caddyserver/certmagic"

	"github.com/nchapman/mizu/internal/config"
)

// TLSStatus is the small read-model the admin wizard polls while
// CertMagic issues a certificate. State transitions are one-way
// off → issuing → ready (or → error). The wizard surfaces Error
// verbatim, so it should be operator-readable.
type TLSStatus struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// TLSManager owns the certmagic.Config and the two listeners (HTTPS +
// ACME-challenge/redirect). It can be brought up at boot (if the
// loaded config has tls.enabled = true) or live from the setup
// wizard via Enable.
//
// The plain-HTTP listener bound on cfg.Server.Addr (e.g. :8080) is
// left running in parallel: the wizard finishes over that listener,
// and once TLS is up the SPA redirects clients to https://. We do not
// shut down :8080 here because doing so mid-request would close the
// wizard's own connection. Operators who want HTTPS-only can drop the
// plain listener via a graceful restart after setup completes.
type TLSManager struct {
	cfg     *config.Config
	handler http.Handler
	wg      *sync.WaitGroup

	mu       sync.Mutex
	state    string // "off", "issuing", "ready", "error"
	lastErr  error
	main     *http.Server
	redirect *http.Server
}

// NewTLSManager constructs an idle manager. The caller wires this in
// during boot and then either:
//
//   - calls EnableFromConfig() if cfg.Server.TLS.Enabled is true at
//     startup (legacy path: TLS pre-configured before this process
//     ever ran), or
//   - leaves it idle and lets the wizard call Enable() once setup
//     completes.
//
// handler is the same chi router the plain HTTP listener serves —
// TLS just terminates in front of it.
func NewTLSManager(handler http.Handler, cfg *config.Config, wg *sync.WaitGroup) *TLSManager {
	return &TLSManager{cfg: cfg, handler: handler, wg: wg, state: "off"}
}

// SetHandler swaps the chi router after construction so main.go can
// hand the manager an early reference (nil) and fill it in once the
// router is built. Safe to call once before Enable; calling after
// Enable has no effect on already-bound listeners.
func (m *TLSManager) SetHandler(h http.Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handler = h
}

// EnableFromConfig brings TLS up using whatever's already in cfg.
// Used at boot when the operator has TLS pre-configured. Returns
// after listeners are bound; issuance runs in the background.
func (m *TLSManager) EnableFromConfig(ctx context.Context) error {
	t := m.cfg.Server.TLS
	return m.Enable(ctx, t.Domains, t.Email, t.Staging)
}

// Enable persists no state itself (the caller is responsible for
// writing the new tls.* block to config.yml before calling); it just
// binds the listeners and kicks off CertMagic. Idempotent in the
// sense that calling Enable a second time with the same state returns
// an error rather than re-binding.
func (m *TLSManager) Enable(ctx context.Context, domains []string, email string, staging bool) error {
	if len(domains) == 0 {
		return errors.New("tls: at least one domain required")
	}
	if strings.TrimSpace(email) == "" {
		return errors.New("tls: ACME contact email required")
	}
	m.mu.Lock()
	if m.state == "issuing" || m.state == "ready" {
		m.mu.Unlock()
		return errors.New("tls: already enabled")
	}
	m.state = "issuing"
	m.lastErr = nil
	m.mu.Unlock()

	certmagic.DefaultACME.Email = email
	certmagic.DefaultACME.Agreed = true
	if staging {
		certmagic.DefaultACME.CA = certmagic.LetsEncryptStagingCA
	} else {
		certmagic.DefaultACME.CA = certmagic.LetsEncryptProductionCA
	}
	certmagic.Default.Storage = &certmagic.FileStorage{Path: m.cfg.Paths.Certs}
	magic := certmagic.NewDefault()
	tc := magic.TLSConfig()
	tc.NextProtos = append([]string{"h2", "http/1.1"}, tc.NextProtos...)

	addr := m.cfg.Server.TLS.Addr
	if addr == "" {
		addr = ":443"
	}
	httpAddr := m.cfg.Server.TLS.HTTPAddr
	if httpAddr == "" {
		httpAddr = ":80"
	}
	// Catch the most common boot-time misconfig early, with a clear
	// message — otherwise the conflict surfaces as an "address already
	// in use" from a goroutine and the wizard's enable-tls handler is
	// stuck waiting for the listener to come up.
	if m.cfg.Server.Addr == addr || m.cfg.Server.Addr == httpAddr {
		m.recordError(fmt.Errorf("tls: server.addr (%s) conflicts with the TLS listener; move the plain HTTP listener to a different port", m.cfg.Server.Addr))
		return fmt.Errorf("tls: server.addr (%s) overlaps the TLS port; pick a different server.addr (e.g. :8080)", m.cfg.Server.Addr)
	}

	mainSrv := &http.Server{
		Addr:              addr,
		Handler:           m.handler,
		TLSConfig:         tc,
		ReadHeaderTimeout: m.cfg.Limits.ReadHeaderTimeout,
		ReadTimeout:       m.cfg.Limits.ReadTimeout,
		WriteTimeout:      m.cfg.Limits.WriteTimeout,
		IdleTimeout:       m.cfg.Limits.IdleTimeout,
		MaxHeaderBytes:    m.cfg.Limits.MaxHeaderBytes,
	}
	redirect := &http.Server{
		Addr:              httpAddr,
		Handler:           buildHTTPHandler(magic, domains),
		ReadHeaderTimeout: m.cfg.Limits.ReadHeaderTimeout,
		ReadTimeout:       m.cfg.Limits.ReadTimeout,
		WriteTimeout:      m.cfg.Limits.WriteTimeout,
		IdleTimeout:       m.cfg.Limits.IdleTimeout,
		MaxHeaderBytes:    m.cfg.Limits.MaxHeaderBytes,
	}

	m.mu.Lock()
	m.main = mainSrv
	m.redirect = redirect
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		log.Printf("mizu listening on %s (https) for %v", mainSrv.Addr, domains)
		if err := mainSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Printf("https server: %v", err)
			m.recordError(fmt.Errorf("https listener: %w", err))
		}
	}()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		log.Printf("mizu listening on %s (http: ACME + redirect)", redirect.Addr)
		if err := redirect.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http redirect server: %v", err)
			m.recordError(fmt.Errorf("http redirect listener: %w", err))
		}
	}()

	// ManageAsync drives issuance in the background. We can't reliably
	// detect "issued and trusted" without polling the certmagic cache,
	// so we flip to "ready" optimistically once ManageAsync returns:
	// the listener is bound, the cert will materialize when ACME
	// resolves. A real-world miss (e.g. DNS misconfigured) surfaces as
	// a TLS handshake error when the user follows the wizard's redirect.
	if err := magic.ManageAsync(ctx, domains); err != nil {
		m.recordError(err)
		return err
	}
	m.mu.Lock()
	if m.state == "issuing" {
		m.state = "ready"
	}
	m.mu.Unlock()
	return nil
}

func (m *TLSManager) recordError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = "error"
	m.lastErr = err
}

// Status reports the current state for the wizard's poll loop.
func (m *TLSManager) Status() TLSStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := TLSStatus{State: m.state}
	if m.lastErr != nil {
		s.Error = m.lastErr.Error()
	}
	return s
}

// Shutdown stops the redirect server and the main HTTPS server. The
// process-wide WaitGroup the caller passed at construction is what
// actually drains the goroutines; this just signals them.
func (m *TLSManager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	main, redirect := m.main, m.redirect
	m.mu.Unlock()
	if redirect != nil {
		_ = redirect.Shutdown(ctx)
	}
	if main != nil {
		_ = main.Shutdown(ctx)
	}
}

// buildHTTPHandler returns the handler for port 80: it serves ACME
// HTTP-01 challenges and 308-redirects everything else to the
// canonical HTTPS URL. The Host header is validated against the
// configured domain list to avoid reflecting an attacker-supplied
// hostname back as an open redirect.
func buildHTTPHandler(magic *certmagic.Config, domains []string) http.Handler {
	allowed := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		allowed[strings.ToLower(strings.TrimSuffix(d, "."))] = struct{}{}
	}
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if _, ok := allowed[host]; !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Connection", "close")
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
	for _, iss := range magic.Issuers {
		if am, ok := iss.(*certmagic.ACMEIssuer); ok {
			return am.HTTPChallengeHandler(redirect)
		}
	}
	return redirect
}
