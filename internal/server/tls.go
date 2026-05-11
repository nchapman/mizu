package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"

	"github.com/nchapman/mizu/internal/config"
)

// TLSStatus is the read-model the admin UI polls. State transitions:
//
//	off → issuing → ready
//	            ↘ error
//
// "issuing" covers everything between the operator hitting "Enable
// HTTPS" and the first successful cert. If DNS hasn't propagated yet,
// CertMagic retries on its own backoff and we stay in "issuing" with
// the latest validation error surfaced in `Error` for the UI to show.
// "error" is reserved for hard failures (listener bind, port conflict)
// that CertMagic can't recover from; the operator must retry by hand.
// "ready" flips when CertMagic fires the `cert_obtained` event.
type TLSStatus struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// TLSManager owns the certmagic.Config and the two listeners (HTTPS +
// ACME-challenge/redirect). It can be brought up at boot (if the
// loaded config has tls.enabled = true) or live from the setup wizard
// via Enable.
//
// The plain-HTTP listener bound on cfg.Server.Addr (e.g. :8080) is
// left running in parallel: the wizard finishes over that listener,
// and once TLS is up the SPA redirects clients to https://.
type TLSManager struct {
	cfg     *config.Config
	handler http.Handler
	wg      *sync.WaitGroup

	mu        sync.Mutex
	state     string
	lastErr   error
	onEnabled func(domains []string, email string, staging bool)

	// magic is retained on the struct so its certificate cache + the
	// maintenance goroutine the cache spawns stay alive for the process
	// lifetime. Renewals fire automatically off that goroutine — there's
	// nothing else to schedule.
	magic    *certmagic.Config
	cache    *certmagic.Cache
	main     *http.Server
	redirect *http.Server
}

// NewTLSManager constructs an idle manager. handler is the same chi
// router the plain HTTP listener serves — TLS just terminates in front
// of it.
func NewTLSManager(handler http.Handler, cfg *config.Config, wg *sync.WaitGroup) *TLSManager {
	return &TLSManager{cfg: cfg, handler: handler, wg: wg, state: "off"}
}

// SetHandler swaps the chi router after construction so main.go can
// hand the manager an early reference (nil) and fill it in once the
// router is built. Safe to call once before Enable.
func (m *TLSManager) SetHandler(h http.Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handler = h
}

// OnEnabled registers a callback fired once CertMagic obtains a cert
// (the `cert_obtained` event). Admin uses this to persist
// cfg.Server.TLS to disk only after a real cert lands, so a failed
// issuance can't leave enabled=true on disk and Fatalf the next
// restart. The callback also fires on renewal — re-writing the same
// values is a no-op so that's fine.
func (m *TLSManager) OnEnabled(fn func(domains []string, email string, staging bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onEnabled = fn
}

// EnableFromConfig brings TLS up using whatever's already in cfg.
// Used at boot when the operator has TLS pre-configured. Returns
// after listeners are bound; issuance runs in the background via
// CertMagic's own retry/backoff.
func (m *TLSManager) EnableFromConfig(ctx context.Context) error {
	t := m.cfg.Server.TLS
	return m.Enable(ctx, t.Domains, t.Email, t.Staging)
}

// Enable binds the HTTPS + ACME listeners and hands the domain off to
// CertMagic. CertMagic handles DNS-not-ready, transient ACME errors,
// rate-limit backoff, and renewals on its own — we just need to start
// it once and stay out of the way.
//
// Returns synchronously after listeners are bound. Cert issuance is
// asynchronous; subscribe to Status() to watch the state transition
// from "issuing" → "ready" (or to surface a transient `Error`).
func (m *TLSManager) Enable(ctx context.Context, domains []string, email string, staging bool) error {
	if len(domains) == 0 {
		return errors.New("tls: at least one domain required")
	}
	if strings.TrimSpace(email) == "" {
		return errors.New("tls: ACME contact email required")
	}

	m.mu.Lock()
	if m.handler == nil {
		m.mu.Unlock()
		// Belt-and-braces guard. If main.go ever forgets to wire
		// SetHandler before Enable, net/http would silently fall back
		// to DefaultServeMux and serve a confusing empty 404 over HTTPS
		// instead of erroring loudly. Fail fast.
		return errors.New("tls: handler not set; call SetHandler before Enable")
	}
	if m.state == "issuing" || m.state == "ready" {
		m.mu.Unlock()
		return errors.New("tls: already enabled")
	}
	handler := m.handler
	m.state = "issuing"
	m.lastErr = nil
	m.mu.Unlock()

	addr := m.cfg.Server.TLS.Addr
	if addr == "" {
		addr = ":443"
	}
	httpAddr := m.cfg.Server.TLS.HTTPAddr
	if httpAddr == "" {
		httpAddr = ":80"
	}
	// Catch the most common boot-time misconfig early: the plain HTTP
	// listener can't share a port with the TLS or ACME listener. Without
	// this the conflict would surface as an "address already in use"
	// from inside a goroutine and the UI would just see state="error"
	// with a cryptic message.
	if m.cfg.Server.Addr == addr || m.cfg.Server.Addr == httpAddr {
		m.recordError(fmt.Errorf("tls: server.addr (%s) conflicts with the TLS listener; move the plain HTTP listener to a different port", m.cfg.Server.Addr))
		return fmt.Errorf("tls: server.addr (%s) overlaps the TLS port; pick a different server.addr (e.g. :8080)", m.cfg.Server.Addr)
	}

	ca := certmagic.LetsEncryptProductionCA
	if staging {
		ca = certmagic.LetsEncryptStagingCA
	}

	// Build a non-global certmagic config so we don't stomp on the
	// package-level Default (tests / future callers can coexist) and
	// hold our own cache reference. The OnEvent hooks are how we drive
	// the UI: cert_obtained flips state to "ready" and triggers the
	// config-persist callback; cert_failed updates the surfaced error
	// without changing state (CertMagic owns retry).
	var magic *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			return magic, nil
		},
	})
	magic = certmagic.New(cache, certmagic.Config{
		Storage: &certmagic.FileStorage{Path: m.cfg.Paths.Certs},
		OnEvent: func(_ context.Context, event string, data map[string]any) error {
			switch event {
			case "cert_obtained":
				m.mu.Lock()
				m.state = "ready"
				m.lastErr = nil
				cb := m.onEnabled
				m.mu.Unlock()
				log.Printf("tls: cert obtained for %v", data["identifier"])
				if cb != nil {
					cb(domains, email, staging)
				}
			case "cert_failed":
				if err, ok := data["error"].(error); ok && err != nil {
					m.mu.Lock()
					m.lastErr = err
					m.mu.Unlock()
					log.Printf("tls: cert_failed for %v: %v", data["identifier"], err)
				}
			}
			return nil
		},
	})
	magic.Issuers = []certmagic.Issuer{
		certmagic.NewACMEIssuer(magic, certmagic.ACMEIssuer{
			Email:  email,
			Agreed: true,
			CA:     ca,
		}),
	}

	tc := magic.TLSConfig()
	tc.NextProtos = append([]string{"h2", "http/1.1"}, tc.NextProtos...)

	mainSrv := &http.Server{
		Addr:              addr,
		Handler:           handler,
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
	m.magic = magic
	m.cache = cache
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

	if err := magic.ManageAsync(ctx, domains); err != nil {
		// ManageAsync failed but the two listener goroutines are
		// already running on :80 and :443. Releasing those ports here
		// matters: without the shutdown, a Retry from the UI would
		// call Enable again and fail with "address already in use"
		// because the prior listeners still hold the sockets.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = mainSrv.Shutdown(shutdownCtx)
		_ = redirect.Shutdown(shutdownCtx)
		cancel()
		m.mu.Lock()
		m.main = nil
		m.redirect = nil
		m.mu.Unlock()
		m.recordError(err)
		return err
	}
	return nil
}

// recordError surfaces a failure to the UI. The state transition is
// asymmetric on purpose: from "off" or "issuing" we flip to "error"
// (operator needs to intervene), but from "ready" we only update
// lastErr and log — a transient listener wobble or a renewal-window
// challenge failure shouldn't downgrade a working install to a red
// "error" state. If the cert truly stops working the operator will
// see it in the browser long before this matters, and the next
// renewal attempt's cert_obtained / cert_failed event will refresh
// lastErr cleanly.
func (m *TLSManager) recordError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastErr = err
	if m.state != "ready" {
		m.state = "error"
	}
}

// Status reports the current state for the wizard's poll loop and the
// Settings panel's status indicator.
func (m *TLSManager) Status() TLSStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := TLSStatus{State: m.state}
	if m.lastErr != nil {
		s.Error = m.lastErr.Error()
	}
	return s
}

// Shutdown stops the redirect server, the HTTPS server, and the
// CertMagic cache's maintenance goroutine. The process-wide WaitGroup
// drains the listener goroutines; this just signals them.
func (m *TLSManager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	main, redirect, cache := m.main, m.redirect, m.cache
	m.mu.Unlock()
	if redirect != nil {
		_ = redirect.Shutdown(ctx)
	}
	if main != nil {
		_ = main.Shutdown(ctx)
	}
	if cache != nil {
		cache.Stop()
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
