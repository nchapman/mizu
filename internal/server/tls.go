package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/caddyserver/certmagic"

	"github.com/nchapman/mizu/internal/config"
)

// TLSStatus is the read-model the admin UI polls. State transitions:
//
//	selfsigned → issuing → ready
//	                    ↘ error
//
// "selfsigned" is the boot state — the HTTPS listener is live, serving
// the persistent self-signed cert generated under cfg.Paths.Certs. The
// operator's first visit is encrypted from byte one (with a click-
// through cert warning) so the account-creation password is never sent
// in cleartext.
//
// "issuing" covers everything between EnableACME being called (boot or
// wizard) and the first cert_obtained event from CertMagic. DNS-not-
// ready, transient ACME failures, rate-limit backoff — all of those
// stay in "issuing" with the latest validation error in `Error`.
// CertMagic owns the retries; we just surface them.
//
// "ready" flips when CertMagic has a real cert in cache (either fresh
// from cert_obtained, or already cached from a previous run). The
// HSTS predicate flips true at the same time.
//
// "error" is reserved for hard failures (listener bind, malformed
// config) that CertMagic can't recover from.
type TLSStatus struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// TLSManager owns the always-on HTTPS listener and the CertMagic
// instance that may layer a real Let's Encrypt cert on top of the
// self-signed bootstrap.
//
// Listener lifecycle:
//   - Start(ctx) at boot binds the HTTPS listener. The *tls.Config's
//     GetCertificate is layered: CertMagic's cache first, self-signed
//     fallback if nothing is cached for the SNI. Plain :80 is owned by
//     main.go and always 308-redirects to https on the same Host.
//   - EnableACME(...) hands a domain off to CertMagic. No listener
//     manipulation, no plain-handler swap — the listener is already up.
//   - Shutdown(ctx) signals the HTTPS listener and stops the cache's
//     maintenance goroutine.
type TLSManager struct {
	cfg     *config.Config
	handler http.Handler
	wg      *sync.WaitGroup

	mu      sync.Mutex
	state   string
	lastErr error
	// onEnabled fires once CertMagic confirms a cert via cert_obtained.
	// Admin uses it to persist tls.acme.* to config.yml only after a
	// real cert has actually been obtained, so a failed issuance can't
	// leave enabled=true on disk and Fatalf the next restart.
	onEnabled func(domains []string, email string, staging bool)

	// magic and cache are retained so the cert cache + its maintenance
	// goroutine stay alive for the process lifetime. Renewals fire
	// automatically off that goroutine.
	magic *certmagic.Config
	cache *certmagic.Cache
	main  *http.Server

	// selfSigned is the bootstrap cert returned by the layered
	// GetCertificate when CertMagic doesn't have one cached. Held in an
	// atomic.Pointer so a future "rotate self-signed" operation can
	// swap it without restarting the listener.
	selfSigned atomic.Pointer[tls.Certificate]

	// hasRealCert flips true once CertMagic confirms a real cert is
	// available (either from a fresh cert_obtained event or from a
	// cache pre-populated by a previous run). Read by HSTS middleware.
	hasRealCert atomic.Bool

	// acmeDomains/Email/Staging are the operator-supplied args from
	// the most recent EnableACME call. Stored here (rather than read
	// out of m.cfg) so the cert_obtained event handler can pass them
	// to PersistACMEConfig — which is the very thing that updates
	// m.cfg, so reading from cfg in that callback would be stale.
	// All three are guarded by m.mu.
	acmeDomains []string
	acmeEmail   string
	acmeStaging bool
	// issuerInstalled flips true the first time EnableACME assigns
	// magic.Issuers. Subsequent EnableACME calls with the same args
	// no-op via the state guard; calls with new args refuse — see
	// the comment in EnableACME for the reasoning.
	issuerInstalled bool
}

// NewTLSManager constructs the manager. handler is the chi router the
// HTTPS listener serves. Call Start(ctx) before serving.
func NewTLSManager(handler http.Handler, cfg *config.Config, wg *sync.WaitGroup) *TLSManager {
	return &TLSManager{
		cfg: cfg, handler: handler, wg: wg, state: "selfsigned",
	}
}

// OnEnabled registers a callback fired once CertMagic obtains a cert
// (the cert_obtained event, including renewals).
func (m *TLSManager) OnEnabled(fn func(domains []string, email string, staging bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onEnabled = fn
}

// HasRealCert reports whether CertMagic has a real (non-self-signed)
// certificate available. Used by the HSTS middleware so we never pin
// the bootstrap cert in browsers.
func (m *TLSManager) HasRealCert() bool {
	return m.hasRealCert.Load()
}

// Start binds the HTTPS listener with a layered certificate resolver
// (CertMagic cache → self-signed fallback) and brings the CertMagic
// instance + cert cache online. If the loaded config has ACME enabled,
// also kicks off domain management; otherwise the manager sits in
// "selfsigned" until EnableACME is called from the wizard.
//
// Returns synchronously after the listener is bound. Cert issuance is
// asynchronous; subscribe to Status() to watch state transitions.
func (m *TLSManager) Start(ctx context.Context) error {
	if m.handler == nil {
		return errors.New("tls: handler not set")
	}

	cert, err := LoadOrCreateSelfSigned(SelfSignedCertOptions{
		Dir:        selfSignedDir(m.cfg),
		ExtraHosts: []string{HostFromBaseURL(m.cfg.Site.BaseURL)},
		// IP SANs are intentionally limited to loopback for the bootstrap
		// cert; the public IP isn't reliably known at this point in boot
		// and `localhost`/loopback covers the LAN/SSH-tunnel case. Once
		// ACME issues a real cert it'll cover the configured domain.
	})
	if err != nil {
		return fmt.Errorf("tls: self-signed bootstrap: %w", err)
	}
	m.selfSigned.Store(cert)

	magic, cache := m.buildCertMagic()
	m.mu.Lock()
	m.magic = magic
	m.cache = cache
	m.mu.Unlock()

	addr := m.cfg.Server.TLS.Addr
	if addr == "" {
		addr = ":8443"
	}
	tc := m.buildTLSConfig(magic)

	// Bind the listener up front (rather than relying on
	// http.Server.ListenAndServeTLS) so the resolved address — including
	// the OS-assigned port when addr is `:0` — is available immediately
	// for tests, status output, and future log lines.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("tls: bind %s: %w", addr, err)
	}
	tlsLn := tls.NewListener(ln, tc)

	mainSrv := &http.Server{
		Addr:              ln.Addr().String(),
		Handler:           m.handler,
		TLSConfig:         tc,
		ReadHeaderTimeout: m.cfg.Limits.ReadHeaderTimeout,
		ReadTimeout:       m.cfg.Limits.ReadTimeout,
		WriteTimeout:      m.cfg.Limits.WriteTimeout,
		IdleTimeout:       m.cfg.Limits.IdleTimeout,
		MaxHeaderBytes:    m.cfg.Limits.MaxHeaderBytes,
	}
	m.mu.Lock()
	m.main = mainSrv
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		log.Printf("mizu listening on %s (https; bootstrap self-signed until ACME issues a real cert)", mainSrv.Addr)
		if err := mainSrv.Serve(tlsLn); err != nil && err != http.ErrServerClosed {
			log.Printf("https server: %v", err)
			m.recordError(fmt.Errorf("https listener: %w", err))
		}
	}()

	if m.cfg.Server.TLS.ACME.Enabled {
		acme := m.cfg.Server.TLS.ACME
		if err := m.EnableACME(ctx, acme.Domains, acme.Email, acme.Staging); err != nil {
			// Don't kill the process — the listener is up on self-signed.
			// The operator can retry from the wizard / settings.
			log.Printf("tls: boot-time ACME enable failed (continuing on self-signed): %v", err)
		}
	}
	return nil
}

// EnableACME hands a domain off to CertMagic. Idempotent on the same
// args — calling while already issuing is a no-op. CertMagic owns
// retries, backoff, and renewal; this function returns synchronously
// after kicking off management. Watch Status() for the issuance to
// land.
//
// Issuer install-once: magic.Issuers is set exactly once, on the first
// successful EnableACME call. CertMagic's renewal goroutine reads
// cfg.Issuers without locking (just `make+copy`), so mutating Issuers
// after the cert cache has gone live is a memory-model race even though
// the slice header read is atomic on x86/arm64. A second EnableACME
// with different email/staging is rejected with an error rather than
// silently re-assigning — operators who need to change those must
// restart the process so the next boot constructs a fresh magic.Config.
func (m *TLSManager) EnableACME(ctx context.Context, domains []string, email string, staging bool) error {
	if len(domains) == 0 {
		return errors.New("tls: at least one domain required")
	}
	if strings.TrimSpace(email) == "" {
		return errors.New("tls: ACME contact email required")
	}

	m.mu.Lock()
	if m.magic == nil {
		m.mu.Unlock()
		return errors.New("tls: Start has not been called")
	}
	if m.state == "issuing" || m.state == "ready" {
		m.mu.Unlock()
		return nil
	}
	if m.issuerInstalled && (m.acmeEmail != email || m.acmeStaging != staging) {
		// First call already wired magic.Issuers; mutating it now would
		// race with a maintenance/renewal goroutine. Make the operator
		// restart instead.
		m.mu.Unlock()
		return errors.New("tls: ACME email/staging changed — restart the process to apply")
	}
	magic := m.magic
	if !m.issuerInstalled {
		ca := certmagic.LetsEncryptProductionCA
		if staging {
			ca = certmagic.LetsEncryptStagingCA
		}
		magic.Issuers = []certmagic.Issuer{
			certmagic.NewACMEIssuer(magic, certmagic.ACMEIssuer{
				Email:  email,
				Agreed: true,
				CA:     ca,
			}),
		}
		m.issuerInstalled = true
	}
	m.acmeDomains = append(m.acmeDomains[:0:0], domains...) // defensive copy
	m.acmeEmail = email
	m.acmeStaging = staging
	m.state = "issuing"
	m.lastErr = nil
	cb := m.onEnabled
	m.mu.Unlock()

	if err := magic.ManageAsync(ctx, domains); err != nil {
		m.recordError(err)
		return err
	}

	// If a previous run already obtained a cert, CertMagic loads it
	// into the cache during ManageAsync but does NOT fire
	// cert_obtained. Probe the cache so the wizard's status display
	// (and HSTS) reflect reality immediately. The cb fires here AND
	// from cert_obtained on renewal — PersistACMEConfig is idempotent
	// (writes the same bytes), so the duplicate is harmless.
	if m.certAlreadyCached(domains) {
		m.mu.Lock()
		m.state = "ready"
		m.mu.Unlock()
		m.hasRealCert.Store(true)
		if cb != nil {
			cb(domains, email, staging)
		}
	}
	return nil
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

// Shutdown stops the HTTPS server and the CertMagic cache's maintenance
// goroutine. The process-wide WaitGroup drains the listener goroutine.
func (m *TLSManager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	main, cache := m.main, m.cache
	m.mu.Unlock()
	if main != nil {
		_ = main.Shutdown(ctx)
	}
	if cache != nil {
		cache.Stop()
	}
}

// PlainRedirectHandler is the always-on plain-:80 handler. The inner
// handler 308-redirects every request to https on the same Host;
// attacker-supplied Host headers only redirect the attacker to
// themselves (no auth context here). When EnableACME has installed an
// ACME issuer, HTTP-01 challenge requests for
// /.well-known/acme-challenge/<token> are intercepted before the
// redirect.
//
// The ACME wrap is consulted PER REQUEST so it picks up the issuer
// installed by a wizard-driven EnableACME after this handler was
// constructed. main.go wires this handler before tlsMgr.Start runs;
// snapshotting magic.Issuers at construction would freeze it as nil.
func (m *TLSManager) PlainRedirectHandler() http.Handler {
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if host == "" {
			http.Error(w, "missing Host", http.StatusBadRequest)
			return
		}
		// Strip any explicit port: the redirect target is always the
		// implicit :443 (or whatever the host has fronted us with).
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		w.Header().Set("Connection", "close")
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if iss := m.acmeIssuer(); iss != nil {
			iss.HTTPChallengeHandler(redirect).ServeHTTP(w, r)
			return
		}
		redirect.ServeHTTP(w, r)
	})
}

// acmeIssuer returns the currently-installed ACME issuer, or nil if
// EnableACME hasn't been called yet. Held under m.mu; the issuer
// pointer itself is install-once (see EnableACME) so the returned
// value is safe to use without a lock.
func (m *TLSManager) acmeIssuer() *certmagic.ACMEIssuer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.magic == nil {
		return nil
	}
	for _, iss := range m.magic.Issuers {
		if am, ok := iss.(*certmagic.ACMEIssuer); ok {
			return am
		}
	}
	return nil
}

// recordError surfaces a failure to the UI. From "selfsigned" or
// "issuing" we flip to "error" — the operator needs to retry. From
// "ready" we only update lastErr — a transient renewal failure
// shouldn't downgrade a working install.
func (m *TLSManager) recordError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastErr = err
	if m.state != "ready" {
		m.state = "error"
	}
}

// buildCertMagic constructs the CertMagic config + cache. The OnEvent
// hooks are how we drive the UI: cert_obtained flips state to "ready"
// and triggers the persist callback; cert_failed updates the surfaced
// error without changing state (CertMagic owns retry).
func (m *TLSManager) buildCertMagic() (*certmagic.Config, *certmagic.Cache) {
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
				domains, email, staging := m.acmeArgsLocked()
				m.mu.Unlock()
				m.hasRealCert.Store(true)
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
	return magic, cache
}

// acmeArgsLocked returns the operator-supplied ACME arguments for the
// persist callback. Caller must hold m.mu. Reads from manager fields
// (set by EnableACME) rather than m.cfg — the persist callback's
// entire job is to update m.cfg, so reading from cfg here would
// observe stale zero-values on the first wizard-issued cert.
func (m *TLSManager) acmeArgsLocked() (domains []string, email string, staging bool) {
	return m.acmeDomains, m.acmeEmail, m.acmeStaging
}

// buildTLSConfig wraps CertMagic's GetCertificate with a self-signed
// fallback. On first request for a name CertMagic doesn't have, the
// listener returns the persistent self-signed cert so the connection
// completes (with a browser warning) instead of dying with "no
// certificate available." Once ACME issues a real cert, CertMagic's
// cache returns it and the fallback is bypassed.
func (m *TLSManager) buildTLSConfig(magic *certmagic.Config) *tls.Config {
	base := magic.TLSConfig()
	cmGet := base.GetCertificate
	base.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if cert, err := cmGet(hello); err == nil && cert != nil {
			return cert, nil
		}
		ss := m.selfSigned.Load()
		if ss == nil {
			return nil, errors.New("tls: no certificate available (self-signed bootstrap missing)")
		}
		return ss, nil
	}
	// Application protocols on top of the ACME-TLS sentinel certmagic
	// pre-populated.
	base.NextProtos = append([]string{"h2", "http/1.1"}, base.NextProtos...)
	return base
}

// certAlreadyCached returns true if CertMagic already has a usable
// cert in cache for any of the supplied domains. After ManageAsync,
// this catches the "cert from previous run was loaded from storage"
// case where cert_obtained never fires.
func (m *TLSManager) certAlreadyCached(domains []string) bool {
	if m.cache == nil {
		return false
	}
	for _, d := range domains {
		if len(m.cache.AllMatchingCertificates(d)) > 0 {
			return true
		}
	}
	return false
}

// selfSignedDir returns the directory under cfg.Paths.Certs where the
// bootstrap self-signed cert is persisted.
func selfSignedDir(cfg *config.Config) string {
	return filepath.Join(cfg.Paths.Certs, "selfsigned")
}
