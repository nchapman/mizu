package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSite_CreatesFileWithDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := WriteSite(path, SiteSettings{
		Title:       "My Blog",
		Author:      "Alice",
		BaseURL:     "https://blog.example",
		Description: "Notes & links.",
		ThemeName:   "default",
	}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"title: My Blog", "author: Alice", "base_url: https://blog.example", "description: Notes & links.", "name: default", "setup wizard"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	// Round-trip through Load to verify the result is valid config YAML.
	t.Chdir(t.TempDir())
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load round-trip: %v", err)
	}
	if c.Site.Title != "My Blog" || c.Site.BaseURL != "https://blog.example" {
		t.Errorf("round-trip mismatch: %+v", c.Site)
	}
}

func TestWriteSite_PreservesExistingComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	original := `# my hand-tuned mizu config
site:
  title: Old Title  # this comment matters
  author: Bob
paths:
  content: ./content
  state: ./state
poller:
  interval: 30m  # tuned to my needs
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSite(path, SiteSettings{
		Title:   "New Title",
		Author:  "Bob",
		BaseURL: "https://example.com",
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	for _, want := range []string{"my hand-tuned mizu config", "this comment matters", "tuned to my needs", "interval: 30m", "title: New Title", "base_url: https://example.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q after edit:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Old Title") {
		t.Errorf("Old Title not overwritten:\n%s", got)
	}
}

func TestWriteTLS_RejectsEnabledWithoutDomain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := WriteTLS(path, TLSSettings{Enabled: true}); err == nil {
		t.Fatal("expected error when enabled without domains")
	}
	if err := WriteTLS(path, TLSSettings{Enabled: true, Domains: []string{"x.example"}}); err == nil {
		t.Fatal("expected error when enabled without email")
	}
}

func TestWriteTLS_HappyPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	// Seed a site block so we can verify it survives the TLS write.
	if err := WriteSite(path, SiteSettings{Title: "x", BaseURL: "https://x"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteTLS(path, TLSSettings{
		Enabled: true, Domains: []string{"a.example", "b.example"}, Email: "ops@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Server.TLS.Enabled || c.Server.TLS.Email != "ops@example.com" {
		t.Errorf("tls=%+v", c.Server.TLS)
	}
	if len(c.Server.TLS.Domains) != 2 {
		t.Errorf("domains=%v", c.Server.TLS.Domains)
	}
	if c.Site.Title != "x" {
		t.Error("TLS write nuked site block")
	}
}
