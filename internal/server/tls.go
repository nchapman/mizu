package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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

// TLSManager owns the certmagic.Config and the HTTPS listener. It can
// be brought up at boot (if the loaded config has tls.enabled = true)
// or live from the setup wizard via Enable.
//
// There is no separate :80 listener inside the container. ACME HTTP-01
// challenges and the HTTP→HTTPS redirect ride on the always-on plain
// listener that main.go owns on cfg.Server.Addr (an internal port,
// host-mapped to :80 by Docker). When Enable succeeds, the manager
// hands main.go a wrapped handler that adds ACME and redirect behavior
// in front of the chi router; main.go installs it via the callback
// registered with OnPlainHandlerChange.
type TLSManager struct {
	cfg     *config.Config
	handler http.Handler
	wg      *sync.WaitGroup

	mu              sync.Mutex
	state           string
	lastErr         error
	onEnabled       func(domains []string, email string, staging bool)
	setPlainHandler func(http.Handler)
	// plainHandlerBase is the unwrapped chi router. Retained on the
	// manager for one reason: if Enable's ManageAsync call fails after
	// we've already swapped the plain listener to the ACME+redirect
	// wrapper, we need the original to swap back to.
	plainHandlerBase http.Handler

	// magic is retained on the struct so its certificate cache + the
	// maintenance goroutine the cache spawns stay alive for the process
	// lifetime. Renewals fire automatically off that goroutine — there's
	// nothing else to schedule.
	magic *certmagic.Config
	cache *certmagic.Cache
	main  *http.Server

	// closed flips once Shutdown begins. Enable consults it under mu
	// before m.wg.Add(1) so a late wizard-flip can't add to the
	// WaitGroup that bg.Wait() is already draining (Add-after-Wait
	// panics on a zero counter; even with a non-zero counter the order
	// of operations here is fragile and worth pinning down).
	closed bool

	// rollingBack tells the HTTPS listener goroutine to skip its
	// recordError call when Enable's sync rollback path is shutting it
	// down. Without this, a bind-failure in the goroutine and the
	// ManageAsync error in the sync path race to set lastErr; we want
	// the ManageAsync error to win because that's the one Enable
	// returns to the API caller.
	rollingBack atomic.Bool
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

// OnPlainHandlerChange registers the callback the manager fires to
// swap the plain-HTTP listener's handler in main.go. base is the
// untreated chi router used when TLS is off; the manager wraps it with
// ACME + redirect when Enable succeeds. Must be called before Enable.
func (m *TLSManager) OnPlainHandlerChange(base http.Handler, fn func(http.Handler)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plainHandlerBase = base
	m.setPlainHandler = fn
}

// EnableFromConfig brings TLS up using whatever's already in cfg.
// Used at boot when the operator has TLS pre-configured. Returns
// after listeners are bound; issuance runs in the background via
// CertMagic's own retry/backoff.
func (m *TLSManager) EnableFromConfig(ctx context.Context) error {
	t := m.cfg.Server.TLS
	return m.Enable(ctx, t.Domains, t.Email, t.Staging)
}

// Enable binds the HTTPS listener, wraps the plain-HTTP handler with
// ACME-challenge + redirect-to-HTTPS behavior, and hands the domain
// off to CertMagic. CertMagic handles DNS-not-ready, transient ACME
// errors, rate-limit backoff, and renewals on its own — we just need
// to start it once and stay out of the way.
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
	if m.closed {
		m.mu.Unlock()
		// Shutdown has begun. Refusing the Add here keeps the
		// process-wide WaitGroup ordering invariant: every Add happens
		// before bg.Wait observes the counter at zero.
		return errors.New("tls: manager shutting down")
	}
	if m.handler == nil {
		m.mu.Unlock()
		// Belt-and-braces guard. If main.go ever forgets to wire
		// SetHandler before Enable, net/http would silently fall back
		// to DefaultServeMux and serve a confusing empty 404 over HTTPS
		// instead of erroring loudly. Fail fast.
		return errors.New("tls: handler not set; call SetHandler before Enable")
	}
	if m.setPlainHandler == nil || m.plainHandlerBase == nil {
		m.mu.Unlock()
		return errors.New("tls: plain handler swap not wired; call OnPlainHandlerChange before Enable")
	}
	if m.state == "issuing" || m.state == "ready" {
		m.mu.Unlock()
		return errors.New("tls: already enabled")
	}
	handler := m.handler
	plainBase := m.plainHandlerBase
	setPlain := m.setPlainHandler
	m.lastErr = nil
	// Reserve the WaitGroup slot now, while still holding mu and
	// before we've done any heavy work. The goroutine that consumes it
	// is started further down; if anything between here and there
	// fails (or panics), defer m.wg.Done in the failure paths is the
	// caller's responsibility — but in practice we only fail before
	// the goroutine starts via the ManageAsync rollback below, which
	// is itself responsible for shutting the goroutine down.
	m.wg.Add(1)
	m.rollingBack.Store(false)
	m.mu.Unlock()

	addr := m.cfg.Server.TLS.Addr
	if addr == "" {
		addr = ":8443"
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

	m.mu.Lock()
	m.magic = magic
	m.cache = cache
	m.main = mainSrv
	m.mu.Unlock()

	// Install the wrapped handler on the plain listener BEFORE the
	// HTTPS listener starts so ACME HTTP-01 challenges (which Let's
	// Encrypt fires inside ManageAsync) hit the challenge handler.
	// Flip state to "issuing" *after* the swap so a Status() poll
	// observing "issuing" can rely on the wrapped handler being live.
	setPlain(buildPlainHandler(magic, domains, plainBase))
	m.mu.Lock()
	m.state = "issuing"
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		log.Printf("mizu listening on %s (https) for %v", mainSrv.Addr, domains)
		err := mainSrv.ListenAndServeTLS("", "")
		if err == nil || err == http.ErrServerClosed {
			return
		}
		log.Printf("https server: %v", err)
		// During Enable's sync rollback the ManageAsync error is the
		// authoritative one — skip recordError here so it doesn't race
		// to overwrite lastErr.
		if m.rollingBack.Load() {
			return
		}
		m.recordError(fmt.Errorf("https listener: %w", err))
	}()

	if err := magic.ManageAsync(ctx, domains); err != nil {
		// ManageAsync failed but the HTTPS listener goroutine is
		// already running. Shut it down so a Retry from the UI can call
		// Enable again without "address already in use", and swap the
		// plain handler back to the bare router.
		m.rollingBack.Store(true)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = mainSrv.Shutdown(shutdownCtx)
		cancel()
		setPlain(plainBase)
		m.mu.Lock()
		m.main = nil
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

// Shutdown stops the HTTPS server and the CertMagic cache's maintenance
// goroutine. The process-wide WaitGroup drains the listener goroutine;
// this just signals it.
//
// Setting closed under mu before reading main/cache means any racing
// Enable call observes closed=true and refuses to wg.Add — keeping the
// "every Add happens before Wait sees the counter at zero" invariant.
func (m *TLSManager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	m.closed = true
	main, cache := m.main, m.cache
	m.mu.Unlock()
	if main != nil {
		_ = main.Shutdown(ctx)
	}
	if cache != nil {
		cache.Stop()
	}
}

// buildPlainHandler wraps the bare router with ACME HTTP-01 challenge
// passthrough and HTTP→HTTPS redirect for requests whose Host matches
// the configured domains. Requests with any other Host (raw IP, LAN
// hostnames) fall through to the base handler so:
//   - the wizard session that just enabled HTTPS doesn't 308 itself to a
//     domain that hasn't propagated yet,
//   - LAN clients hitting the box by IP keep working,
//   - and the Host header is never reflected into the redirect target
//     (an attacker-supplied Host gets passed to the chi router instead).
//
// Security posture for fallthrough: the chi router's auth.Middleware is
// the effective gate for /admin/* — a forged Host that bypasses the
// redirect still has to authenticate to reach anything sensitive. Since
// the session cookie is set Secure once TLS is on, a stolen plain-HTTP
// request can't carry a valid session anyway.
func buildPlainHandler(magic *certmagic.Config, domains []string, base http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		allowed[strings.ToLower(strings.TrimSuffix(d, "."))] = struct{}{}
	}
	core := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if _, ok := allowed[host]; !ok {
			// Not a configured domain — pass through to the app. This
			// preserves bootstrap and LAN access without ever sending a
			// caller-supplied Host into a redirect.
			base.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Connection", "close")
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
	for _, iss := range magic.Issuers {
		if am, ok := iss.(*certmagic.ACMEIssuer); ok {
			return am.HTTPChallengeHandler(core)
		}
	}
	return core
}
