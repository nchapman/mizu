package server

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/caddyserver/certmagic"

	"github.com/nchapman/mizu/internal/config"
)

// TLSRunner manages the two-listener setup needed for automatic HTTPS:
// the main HTTPS server on tls.Addr and a small HTTP server on
// tls.HTTPAddr that solves ACME HTTP-01 challenges and redirects
// everything else to HTTPS. CertMagic itself handles certificate
// issuance, OCSP stapling, and renewal.
type TLSRunner struct {
	main     *http.Server
	redirect *http.Server
}

// StartTLS wires CertMagic against cfg and starts both listeners.
// The returned runner's Shutdown should be called during graceful
// shutdown alongside main.Shutdown for the main server.
//
// The caller's main *http.Server is mutated: its TLSConfig is set
// from the CertMagic configuration. The caller should call neither
// ListenAndServe nor ListenAndServeTLS on it — StartTLS handles that.
//
// Listener ordering matters. We bind port 80 (with the ACME challenge
// handler installed) and port 443 *before* asking CertMagic to obtain
// certificates. ManageAsync then drives issuance in the background:
// during cold-start issuance, the first HTTPS request stalls briefly
// on the handshake, but the process never deadlocks waiting for ACME
// to finish before listeners exist. ManageSync would invert that and
// race CertMagic's own ephemeral :80 listener with anything else on
// the box.
func StartTLS(ctx context.Context, mainSrv *http.Server, cfg *config.Config, wg *sync.WaitGroup) (*TLSRunner, error) {
	tlsCfg := cfg.Server.TLS
	certmagic.DefaultACME.Email = tlsCfg.Email
	certmagic.DefaultACME.Agreed = true
	if tlsCfg.Staging {
		certmagic.DefaultACME.CA = certmagic.LetsEncryptStagingCA
	} else {
		certmagic.DefaultACME.CA = certmagic.LetsEncryptProductionCA
	}
	certmagic.Default.Storage = &certmagic.FileStorage{Path: cfg.Paths.Certs}

	magic := certmagic.NewDefault()

	tc := magic.TLSConfig()
	tc.NextProtos = append([]string{"h2", "http/1.1"}, tc.NextProtos...)
	mainSrv.Addr = tlsCfg.Addr
	mainSrv.TLSConfig = tc

	redirect := &http.Server{
		Addr:              tlsCfg.HTTPAddr,
		Handler:           buildHTTPHandler(magic, tlsCfg.Domains),
		ReadHeaderTimeout: cfg.Limits.ReadHeaderTimeout,
		ReadTimeout:       cfg.Limits.ReadTimeout,
		WriteTimeout:      cfg.Limits.WriteTimeout,
		IdleTimeout:       cfg.Limits.IdleTimeout,
		MaxHeaderBytes:    cfg.Limits.MaxHeaderBytes,
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("mizu listening on %s (https) for %v", mainSrv.Addr, tlsCfg.Domains)
		if err := mainSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Printf("https server: %v", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("mizu listening on %s (http: ACME + redirect)", redirect.Addr)
		if err := redirect.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http redirect server: %v", err)
		}
	}()

	// Listeners are live; now drive issuance in the background. ACME
	// HTTP-01 challenges arrive at the port-80 handler we just bound,
	// where HTTPChallengeHandler intercepts them before the redirect
	// fallthrough.
	if err := magic.ManageAsync(ctx, tlsCfg.Domains); err != nil {
		return nil, err
	}

	return &TLSRunner{main: mainSrv, redirect: redirect}, nil
}

// Shutdown stops the redirect server. The main HTTPS server is owned
// by the caller and shut down separately so that drain timeouts are
// applied uniformly across both code paths.
func (r *TLSRunner) Shutdown(ctx context.Context) {
	if r == nil || r.redirect == nil {
		return
	}
	_ = r.redirect.Shutdown(ctx)
}

// buildHTTPHandler returns the handler for port 80: it serves
// ACME HTTP-01 challenges and 308-redirects everything else to the
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
