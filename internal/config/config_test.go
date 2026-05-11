package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_Happy(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgPath, `
site:
  title: My Blog
  author: Me
  base_url: https://example.com
  description: words
server:
  addr: ":8080"
paths:
  content: `+filepath.Join(dir, "content")+`
  media: `+filepath.Join(dir, "media")+`
  state: `+filepath.Join(dir, "state")+`
poller:
  interval: 30m
  user_agent: test/1.0
`)
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.Site.Title != "My Blog" || c.Site.Author != "Me" {
		t.Errorf("site fields wrong: %+v", c.Site)
	}
	if c.Server.Addr != ":8080" {
		t.Errorf("server addr=%q", c.Server.Addr)
	}
	if c.Poller.Interval != 30*time.Minute {
		t.Errorf("interval=%v", c.Poller.Interval)
	}
	if c.Poller.UserAgent != "test/1.0" {
		t.Errorf("ua=%q", c.Poller.UserAgent)
	}
	for _, d := range []string{
		filepath.Join(dir, "content", "posts"),
		filepath.Join(dir, "content", "drafts"),
		filepath.Join(dir, "media"),
		filepath.Join(dir, "state"),
	} {
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("dir %s not created: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", d)
		}
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgPath, `
paths:
  content: `+filepath.Join(dir, "content")+`
  media: `+filepath.Join(dir, "media")+`
  state: `+filepath.Join(dir, "state")+`
`)
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.Poller.Interval != time.Hour {
		t.Errorf("default interval=%v, want 1h", c.Poller.Interval)
	}
	if c.Poller.UserAgent != "mizu/0.1" {
		t.Errorf("default ua=%q", c.Poller.UserAgent)
	}
	if c.Paths.Subscriptions != "./subscriptions.opml" {
		t.Errorf("default subscriptions=%q", c.Paths.Subscriptions)
	}
	if c.Paths.Certs != filepath.Join(dir, "state", "certs") {
		t.Errorf("default certs=%q", c.Paths.Certs)
	}
	if c.Server.TLS.Addr != ":8443" {
		t.Errorf("default tls addr=%q, want :8443", c.Server.TLS.Addr)
	}
	if c.Limits.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("default read_header_timeout=%v", c.Limits.ReadHeaderTimeout)
	}
	if c.Limits.Body.Login != 1<<10 || c.Limits.Body.Webmention != 4<<10 {
		t.Errorf("default body limits: login=%d webmention=%d", c.Limits.Body.Login, c.Limits.Body.Webmention)
	}
	if c.Limits.Rate.Login.Requests != 10 || c.Limits.Rate.Login.Per != time.Minute {
		t.Errorf("default login rate: %+v", c.Limits.Rate.Login)
	}
}

func TestLoad_TLSValidation(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	base := `
paths:
  content: ` + filepath.Join(dir, "content") + `
  media: ` + filepath.Join(dir, "media") + `
  state: ` + filepath.Join(dir, "state") + `
`
	// Enabled but missing domains and email.
	writeFile(t, cfgPath, base+`
server:
  tls:
    enabled: true
`)
	if _, err := Load(cfgPath); err == nil {
		t.Fatal("expected validation error for tls.enabled without domains/email")
	}

	// Domains set but email missing.
	writeFile(t, cfgPath, base+`
server:
  tls:
    enabled: true
    domains: [example.com]
`)
	if _, err := Load(cfgPath); err == nil {
		t.Fatal("expected validation error for tls.enabled without email")
	}

	// Both set: ok.
	writeFile(t, cfgPath, base+`
server:
  tls:
    enabled: true
    domains: [example.com]
    email: ops@example.com
`)
	if _, err := Load(cfgPath); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}

func TestLoad_MissingFile_StartsFreshInstall(t *testing.T) {
	// A missing config.yml is now a legal "fresh install" state — Load
	// returns a defaulted Config so the binary can boot and the admin
	// wizard can write the real file at the end of onboarding.
	dir := t.TempDir()
	// Anchor relative defaults inside the temp dir so MkdirAll doesn't
	// scribble into the test's cwd.
	t.Chdir(dir)
	c, err := Load("nope.yaml")
	if err != nil {
		t.Fatalf("Load missing file: %v", err)
	}
	if c.Server.Addr == "" || c.Paths.State == "" {
		t.Errorf("defaults not applied: %+v", c)
	}
	if c.Server.TLS.Enabled {
		t.Error("TLS unexpectedly enabled in fresh-install defaults")
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgPath, "site: [oops:\n  not yaml")
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoad_MkdirOverFile(t *testing.T) {
	dir := t.TempDir()
	// Make a regular file at the path that's supposed to be the content dir
	// — MkdirAll will fail there.
	conflict := filepath.Join(dir, "content")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgPath, `
paths:
  content: `+conflict+`
  media: `+filepath.Join(dir, "media")+`
  state: `+filepath.Join(dir, "state")+`
`)
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected mkdir error, got nil")
	}
}
