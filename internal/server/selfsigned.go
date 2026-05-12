package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Self-signed cert lifetime knobs. Long-validity-with-near-expiry-renew
// keeps the operator from re-trusting after every restart while still
// guaranteeing we never serve an expired cert.
const (
	selfSignedValidity   = 3 * 365 * 24 * time.Hour
	selfSignedRenewFloor = 30 * 24 * time.Hour
)

// SelfSignedCertOptions packages the inputs LoadOrCreateSelfSigned needs
// to fill the cert's SAN block. Hosts and IPs may both be empty — the
// resulting cert is still valid for `localhost` and the loopback IPs,
// which covers the developer-laptop case.
type SelfSignedCertOptions struct {
	// Dir is where the cert (cert.pem) and key (key.pem) are persisted.
	// Created with 0o700 if missing; the key file is written 0o600.
	Dir string
	// ExtraHosts and ExtraIPs are added to the SAN list on top of the
	// defaults. Empty entries and duplicates are dropped.
	ExtraHosts []string
	ExtraIPs   []net.IP
}

// LoadOrCreateSelfSigned reads the persisted self-signed cert from
// opts.Dir, regenerating it if absent, within selfSignedRenewFloor of
// expiry, or missing any of the desired SAN entries. The returned
// *tls.Certificate is suitable for handing to a tls.Config's
// GetCertificate fallback.
//
// The intent is "stable enough that the operator only sees one browser
// warning per browser per install" — not "rotates on a schedule." If
// you need to force a rotation, delete the directory.
func LoadOrCreateSelfSigned(opts SelfSignedCertOptions) (*tls.Certificate, error) {
	if opts.Dir == "" {
		return nil, fmt.Errorf("selfsigned: Dir required")
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("selfsigned: mkdir: %w", err)
	}
	certPath := filepath.Join(opts.Dir, "cert.pem")
	keyPath := filepath.Join(opts.Dir, "key.pem")

	wantHosts, wantIPs := buildSANs(opts)
	if cert, ok := loadIfFresh(certPath, keyPath, wantHosts, wantIPs); ok {
		return cert, nil
	}
	return generateAndPersist(certPath, keyPath, opts)
}

// loadIfFresh returns the on-disk cert if it parses, isn't near expiry,
// and covers every SAN entry the caller wants. ANY load/parse error
// triggers regeneration — half-written pairs from a crash mid-write
// (the writes are not atomic) self-heal that way. Non-ENOENT errors are
// logged so a permission/IO problem doesn't silently regenerate forever
// without leaving a trace.
func loadIfFresh(certPath, keyPath string, wantHosts []string, wantIPs []net.IP) (*tls.Certificate, bool) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("selfsigned: load failed, regenerating: %v", err)
		}
		return nil, false
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		log.Printf("selfsigned: parse failed, regenerating: %v", err)
		return nil, false
	}
	if time.Until(leaf.NotAfter) < selfSignedRenewFloor {
		return nil, false
	}
	if !covers(leaf, wantHosts, wantIPs) {
		// Operator changed Site.BaseURL or some other input that feeds
		// the SAN block. Regenerate so the new hostname doesn't trip a
		// name-mismatch warning.
		return nil, false
	}
	cert.Leaf = leaf
	return &cert, true
}

// covers reports whether leaf's SAN block includes every entry in
// wantHosts and wantIPs. Used to decide whether the on-disk cert is
// still suitable after the operator changed config inputs.
func covers(leaf *x509.Certificate, wantHosts []string, wantIPs []net.IP) bool {
	have := make(map[string]struct{}, len(leaf.DNSNames))
	for _, d := range leaf.DNSNames {
		have[strings.ToLower(d)] = struct{}{}
	}
	for _, h := range wantHosts {
		if _, ok := have[strings.ToLower(h)]; !ok {
			return false
		}
	}
	haveIPs := make(map[string]struct{}, len(leaf.IPAddresses))
	for _, ip := range leaf.IPAddresses {
		haveIPs[ip.String()] = struct{}{}
	}
	for _, ip := range wantIPs {
		if _, ok := haveIPs[ip.String()]; !ok {
			return false
		}
	}
	return true
}

func generateAndPersist(certPath, keyPath string, opts SelfSignedCertOptions) (*tls.Certificate, error) {
	// ECDSA P-256, not ed25519 — ed25519 cert signatures aren't
	// supported by the LibreSSL Apple ships in macOS curl (peer
	// "doesn't support any of the certificate's signature algorithms"
	// at handshake), and we want curl-from-the-laptop smoke tests to
	// just work. Browsers handle both equally well; this is a client-
	// compat choice, not a security one.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("selfsigned: keygen: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("selfsigned: serial: %w", err)
	}

	hosts, ips := buildSANs(opts)
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "mizu self-signed"},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(selfSignedValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              hosts,
		IPAddresses:           ips,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("selfsigned: create cert: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("selfsigned: marshal key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := writeFileMode(certPath, certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := writeFileMode(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("selfsigned: reload after write: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("selfsigned: parse leaf: %w", err)
	}
	cert.Leaf = leaf
	return &cert, nil
}

// buildSANs assembles the cert's SAN block: localhost + loopback IPs
// (so curl localhost works without warnings ever changing the cert),
// plus whatever the caller passed. Duplicates and empties are dropped.
func buildSANs(opts SelfSignedCertOptions) ([]string, []net.IP) {
	hostSet := map[string]struct{}{
		"localhost": {},
	}
	for _, h := range opts.ExtraHosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		hostSet[strings.ToLower(h)] = struct{}{}
	}
	hosts := make([]string, 0, len(hostSet))
	for h := range hostSet {
		hosts = append(hosts, h)
	}

	ipSet := map[string]net.IP{
		"127.0.0.1": net.ParseIP("127.0.0.1"),
		"::1":       net.ParseIP("::1"),
	}
	for _, ip := range opts.ExtraIPs {
		if ip == nil {
			continue
		}
		ipSet[ip.String()] = ip
	}
	ips := make([]net.IP, 0, len(ipSet))
	for _, ip := range ipSet {
		ips = append(ips, ip)
	}
	return hosts, ips
}

// HostFromBaseURL extracts just the host part of a configured base_url
// so it can be added to the self-signed cert's SAN block. Returns ""
// for unparseable inputs — callers feed the result into ExtraHosts,
// which drops empty entries.
func HostFromBaseURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func writeFileMode(path string, data []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("selfsigned: write %s: %w", path, err)
	}
	return nil
}
