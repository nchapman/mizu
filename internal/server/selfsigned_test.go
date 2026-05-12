package server

import (
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSelfSigned_GeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	cert, err := LoadOrCreateSelfSigned(SelfSignedCertOptions{
		Dir:        dir,
		ExtraHosts: []string{"mizu.example", "MIZU.example", "  "},
		ExtraIPs:   []net.IP{net.ParseIP("203.0.113.5")},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	leaf := cert.Leaf
	if leaf.Subject.CommonName != "mizu self-signed" {
		t.Errorf("CN=%q", leaf.Subject.CommonName)
	}
	if !slices.Contains(leaf.DNSNames, "localhost") {
		t.Errorf("missing localhost SAN: %v", leaf.DNSNames)
	}
	// Case-folded + deduped: lowercase form is present once, the
	// duplicate uppercase is collapsed, the whitespace entry is dropped.
	count := 0
	for _, d := range leaf.DNSNames {
		if d == "mizu.example" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 mizu.example SAN, got %d in %v", count, leaf.DNSNames)
	}
	hasIP := false
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("203.0.113.5")) {
			hasIP = true
		}
	}
	if !hasIP {
		t.Errorf("missing extra IP SAN: %v", leaf.IPAddresses)
	}

	// Files persisted with the right permissions on the key.
	if info, err := os.Stat(filepath.Join(dir, "key.pem")); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("key.pem mode=%o, want 0600", info.Mode().Perm())
	}
}

func TestSelfSigned_ReusesAcrossLoad(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateSelfSigned(SelfSignedCertOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateSelfSigned(SelfSignedCertOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// Same cert bytes ⇒ browsers don't see a "different cert" prompt
	// after every restart.
	if string(first.Leaf.Raw) != string(second.Leaf.Raw) {
		t.Error("cert regenerated on second load; should reuse on-disk pair")
	}
}

func TestSelfSigned_RegeneratesOnSANDrift(t *testing.T) {
	// Operator changes Site.BaseURL between boots → the on-disk cert no
	// longer covers the new hostname. Regenerate so the next browser
	// visit doesn't trip a name-mismatch warning even though the cert
	// is otherwise still valid.
	dir := t.TempDir()
	first, err := LoadOrCreateSelfSigned(SelfSignedCertOptions{
		Dir:        dir,
		ExtraHosts: []string{"old.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateSelfSigned(SelfSignedCertOptions{
		Dir:        dir,
		ExtraHosts: []string{"old.example", "new.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Leaf.Raw) == string(second.Leaf.Raw) {
		t.Error("expected regeneration when desired SANs grew")
	}
	covered := false
	for _, d := range second.Leaf.DNSNames {
		if d == "new.example" {
			covered = true
		}
	}
	if !covered {
		t.Errorf("regenerated cert missing new SAN: %v", second.Leaf.DNSNames)
	}
}

func TestSelfSigned_RegeneratesOnCorruptedFile(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateSelfSigned(SelfSignedCertOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// Half-corrupted state from a crash mid-write should self-heal.
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("not a cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateSelfSigned(SelfSignedCertOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Leaf.Raw) == string(second.Leaf.Raw) {
		t.Error("expected regeneration after corrupt cert file")
	}
	if time.Until(second.Leaf.NotAfter) < selfSignedRenewFloor {
		t.Errorf("regenerated cert expires too soon: %v", second.Leaf.NotAfter)
	}
}

func TestSelfSigned_LoadsPriorPair(t *testing.T) {
	// Belt-and-braces: x509.Parse round-trip on the persisted bytes
	// must succeed without us re-running generateAndPersist.
	dir := t.TempDir()
	if _, err := LoadOrCreateSelfSigned(SelfSignedCertOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	pem, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pem) == 0 {
		t.Fatal("empty cert.pem")
	}
	// Confirm it parses as a real x509 certificate, just to fail
	// loudly if the writer ever drifts off PEM format.
	cert, err := LoadOrCreateSelfSigned(SelfSignedCertOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParseCertificate(cert.Certificate[0]); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
}

func TestHostFromBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                      "",
		"https://blog.example":  "blog.example",
		"http://x:8080/abc":     "x",
		"not a url":             "",
		"https://[::1]:443/foo": "::1",
	}
	for in, want := range cases {
		if got := HostFromBaseURL(in); got != want {
			t.Errorf("HostFromBaseURL(%q)=%q want %q", in, got, want)
		}
	}
}
