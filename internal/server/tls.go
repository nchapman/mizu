package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"

	"github.com/nchapman/mizu/internal/config"
	mizudb "github.com/nchapman/mizu/internal/db"
)

// TLSStatus is the read-model the admin UI polls. State transitions are:
//
//	off → pending → issuing → ready
//	                 ↘ error
//
// "pending" means the operator has requested HTTPS but DNS doesn't yet
// resolve the target domain to this server's public IP; a background
// poller is checking on a fixed cadence. "issuing" means listeners are
// bound and CertMagic is driving issuance. "ready" means a cert is
// installed (best-effort — see the optimistic flip in Enable).
type TLSStatus struct {
	State   string      `json:"state"`
	Error   string      `json:"error,omitempty"`
	Pending *PendingTLS `json:"pending,omitempty"`
}

// PendingTLS is what the UI shows on the "Waiting for DNS" card. The
// LastChecked timestamp lets the operator confirm the worker is alive;
// LastError surfaces the most recent DNS-mismatch hint without making
// them open the wizard again.
type PendingTLS struct {
	Domains     []string `json:"domains"`
	Email       string   `json:"email"`
	Staging     bool     `json:"staging"`
	LastChecked int64    `json:"last_checked,omitempty"`
	LastError   string   `json:"last_error,omitempty"`
}

// pendingPollInterval is the cadence at which the background poller
// re-checks DNS for a pending intent. One minute trades operator
// patience ("how often is mizu re-checking?") against the cost of a
// per-tick A-record lookup, which is essentially free.
const pendingPollInterval = time.Minute

// pendingKey is the app_meta key under which we serialize pending intent.
const pendingKey = "tls_pending"

// TLSManager owns the certmagic.Config and the two listeners (HTTPS +
// ACME-challenge/redirect). It can be brought up at boot (if the
// loaded config has tls.enabled = true) or live from the setup wizard
// via Enable / RequestEnable.
//
// The plain-HTTP listener bound on cfg.Server.Addr (e.g. :8080) is
// left running in parallel: the wizard finishes over that listener,
// and once TLS is up the SPA redirects clients to https://.
type TLSManager struct {
	cfg        *config.Config
	handler    http.Handler
	wg         *sync.WaitGroup
	db         *mizudb.DB
	publicIPFn func(ctx context.Context) (string, error)
	bgCtx      context.Context

	mu           sync.Mutex
	state        string
	lastErr      error
	pending      *PendingTLS
	pollerCancel context.CancelFunc
	onEnabled    func(domains []string, email string, staging bool)
	main         *http.Server
	redirect     *http.Server
}

// TLSDeps groups the runtime dependencies TLSManager needs beyond the
// config + handler — keeping the constructor signature manageable.
type TLSDeps struct {
	DB       *mizudb.DB
	PublicIP func(ctx context.Context) (string, error)
}

// NewTLSManager constructs an idle manager. bgCtx lives for the
// process lifetime and drives the pending-DNS poller; once it's
// cancelled the poller exits cleanly. handler is the same chi router
// the plain HTTP listener serves — TLS just terminates in front of it.
func NewTLSManager(bgCtx context.Context, handler http.Handler, cfg *config.Config, wg *sync.WaitGroup, deps TLSDeps) *TLSManager {
	return &TLSManager{
		cfg: cfg, handler: handler, wg: wg, state: "off",
		bgCtx: bgCtx, db: deps.DB, publicIPFn: deps.PublicIP,
	}
}

// SetHandler swaps the chi router after construction so main.go can
// hand the manager an early reference (nil) and fill it in once the
// router is built. Safe to call once before Enable.
func (m *TLSManager) SetHandler(h http.Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handler = h
}

// OnEnabled registers a callback fired once Enable succeeds — either
// from the synchronous hot path or from the pending-DNS poller.
// Admin uses this to persist cfg.Server.TLS to disk only after the
// listener is up, so a failed Enable doesn't leave enabled=true on
// disk and Fatalf the next restart.
func (m *TLSManager) OnEnabled(fn func(domains []string, email string, staging bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onEnabled = fn
}

// RestorePending checks app_meta for a previously-persisted intent
// and starts the poller if one is present. Call at boot so an instance
// restart doesn't drop an in-flight pending request on the floor.
func (m *TLSManager) RestorePending(ctx context.Context) error {
	if m.db == nil {
		return nil
	}
	var raw string
	err := m.db.R.QueryRowContext(ctx, `SELECT value FROM app_meta WHERE key = ?`, pendingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read tls_pending: %w", err)
	}
	var p PendingTLS
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		// Don't crash on a malformed row — log and drop it so the
		// operator can re-request through the UI.
		log.Printf("tls: dropping malformed tls_pending row: %v", err)
		_, _ = m.db.W.ExecContext(ctx, `DELETE FROM app_meta WHERE key = ?`, pendingKey)
		return nil
	}
	m.mu.Lock()
	m.pending = &p
	m.state = "pending"
	m.mu.Unlock()
	m.startPoller()
	return nil
}

// EnableFromConfig brings TLS up using whatever's already in cfg.
// Used at boot when the operator has TLS pre-configured. Returns
// after listeners are bound; issuance runs in the background.
func (m *TLSManager) EnableFromConfig(ctx context.Context) error {
	t := m.cfg.Server.TLS
	return m.Enable(ctx, t.Domains, t.Email, t.Staging)
}

// RequestEnable is the wizard/Settings entry point: try to bring TLS
// up now, and if DNS doesn't yet resolve the operator's domain to
// this server's public IP, persist the intent and let the background
// poller retry every minute until DNS catches up. Either way the
// caller gets a synchronous "we heard you" response.
func (m *TLSManager) RequestEnable(ctx context.Context, domains []string, email string, staging bool) error {
	if len(domains) == 0 {
		return errors.New("tls: at least one domain required")
	}
	if strings.TrimSpace(email) == "" {
		return errors.New("tls: ACME contact email required")
	}
	// Replace any prior pending intent. The operator is expressing
	// fresh consent; the old poll loop must die.
	m.cancelPoller()

	ok, hint := m.dnsReady(ctx, domains[0])
	if ok {
		// Hot path: DNS is correct, fire Enable synchronously so the
		// caller gets a meaningful status code (200/500) instead of an
		// async 202 with a poll loop.
		_ = m.clearPending(ctx)
		return m.Enable(ctx, domains, email, staging)
	}

	pending := &PendingTLS{
		Domains:     domains,
		Email:       email,
		Staging:     staging,
		LastChecked: time.Now().Unix(),
		LastError:   hint,
	}
	if err := m.savePending(ctx, pending); err != nil {
		return err
	}
	m.mu.Lock()
	m.pending = pending
	m.state = "pending"
	m.lastErr = nil
	m.mu.Unlock()
	m.startPoller()
	return nil
}

// CancelPending drops a stored intent and stops the poller. No-op when
// nothing is pending. Used by the UI's "stop waiting" affordance.
func (m *TLSManager) CancelPending(ctx context.Context) error {
	m.cancelPoller()
	if err := m.clearPending(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	m.pending = nil
	if m.state == "pending" {
		m.state = "off"
	}
	m.mu.Unlock()
	return nil
}

// dnsReady returns true when domain resolves to one of this host's
// addresses (matching against the public IP discovered via publicIPFn).
// A returned hint is the same plain-English message the wizard's DNS
// check would surface, suitable for stashing on the pending row.
func (m *TLSManager) dnsReady(ctx context.Context, domain string) (bool, string) {
	ip := ""
	if m.publicIPFn != nil {
		ipCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		ip, _ = m.publicIPFn(ipCtx)
		cancel()
	}
	if ip == "" {
		return false, "Could not determine this server's public IP."
	}
	resolver := &net.Resolver{}
	lookupCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ips, err := resolver.LookupIP(lookupCtx, "ip4", domain)
	if err != nil {
		return false, fmt.Sprintf("Could not resolve A record for %s: %v", domain, err)
	}
	for _, a := range ips {
		if a.String() == ip {
			return true, ""
		}
	}
	got := make([]string, 0, len(ips))
	for _, a := range ips {
		got = append(got, a.String())
	}
	return false, fmt.Sprintf("A record points to %s; expected %s.", strings.Join(got, ", "), ip)
}

// startPoller spins up a single background goroutine that retries the
// pending intent every pendingPollInterval. Idempotent: callers can
// invoke after every state change without risking concurrent pollers
// — the previous one is cancelled first.
func (m *TLSManager) startPoller() {
	m.mu.Lock()
	if m.pollerCancel != nil {
		m.pollerCancel()
	}
	ctx, cancel := context.WithCancel(m.bgCtx)
	m.pollerCancel = cancel
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(pendingPollInterval)
		defer t.Stop()
		for {
			if m.tryPending(ctx) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// tryPending attempts the pending intent once. Returns true when the
// poller should stop (either Enable succeeded or pending was cleared
// while we slept). Updates the persisted last_checked/last_error so the
// UI's "Waiting for DNS" card reflects the most recent probe.
func (m *TLSManager) tryPending(ctx context.Context) bool {
	m.mu.Lock()
	p := m.pending
	m.mu.Unlock()
	if p == nil || len(p.Domains) == 0 {
		return true
	}
	ok, hint := m.dnsReady(ctx, p.Domains[0])
	if ok {
		// DNS caught up. Drop pending state, fire Enable, write config
		// via the onEnabled callback. If Enable itself errors we surface
		// it on the manager state — operator sees "error" in the UI and
		// can retry.
		_ = m.clearPending(ctx)
		m.mu.Lock()
		m.pending = nil
		m.mu.Unlock()
		if err := m.Enable(ctx, p.Domains, p.Email, p.Staging); err != nil {
			log.Printf("tls: pending Enable failed: %v", err)
			return true
		}
		m.mu.Lock()
		cb := m.onEnabled
		m.mu.Unlock()
		if cb != nil {
			cb(p.Domains, p.Email, p.Staging)
		}
		log.Printf("tls: pending intent satisfied; HTTPS live for %v", p.Domains)
		return true
	}
	// Still pending — update the breadcrumb on the row.
	p.LastChecked = time.Now().Unix()
	p.LastError = hint
	_ = m.savePending(ctx, p)
	m.mu.Lock()
	m.pending = p
	m.mu.Unlock()
	return false
}

func (m *TLSManager) cancelPoller() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pollerCancel != nil {
		m.pollerCancel()
		m.pollerCancel = nil
	}
}

func (m *TLSManager) savePending(ctx context.Context, p *PendingTLS) error {
	if m.db == nil {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = m.db.W.ExecContext(ctx,
		`INSERT INTO app_meta(key, value) VALUES(?, ?)
		   ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		pendingKey, string(b),
	)
	return err
}

func (m *TLSManager) clearPending(ctx context.Context) error {
	if m.db == nil {
		return nil
	}
	_, err := m.db.W.ExecContext(ctx, `DELETE FROM app_meta WHERE key = ?`, pendingKey)
	return err
}

// Enable binds the listeners and kicks off CertMagic. The caller is
// responsible for persisting cfg.Server.TLS to disk after this
// returns successfully (admin does this via the OnEnabled callback so
// the same write applies whether Enable was driven by the wizard's
// synchronous path or by the background poller).
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

// Status reports the current state for the wizard's poll loop and the
// Settings panel's status indicator.
func (m *TLSManager) Status() TLSStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := TLSStatus{State: m.state}
	if m.lastErr != nil {
		s.Error = m.lastErr.Error()
	}
	if m.pending != nil {
		pcopy := *m.pending
		// Don't leak the operator's ACME email into the read-model —
		// keep it server-side only (it's not secret but there's no
		// reason to surface it). Domain + breadcrumb fields are fine.
		pcopy.Email = ""
		s.Pending = &pcopy
	}
	return s
}

// Shutdown stops the redirect server and the main HTTPS server. The
// process-wide WaitGroup the caller passed at construction is what
// actually drains the goroutines; this just signals them.
func (m *TLSManager) Shutdown(ctx context.Context) {
	m.cancelPoller()
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
