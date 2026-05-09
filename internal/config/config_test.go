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
  cache: `+filepath.Join(dir, "cache")+`
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
		filepath.Join(dir, "cache"),
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
  cache: `+filepath.Join(dir, "cache")+`
  state: `+filepath.Join(dir, "state")+`
`)
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.Poller.Interval != time.Hour {
		t.Errorf("default interval=%v, want 1h", c.Poller.Interval)
	}
	if c.Poller.UserAgent != "repeat/0.1" {
		t.Errorf("default ua=%q", c.Poller.UserAgent)
	}
	if c.Paths.Subscriptions != "./subscriptions.opml" {
		t.Errorf("default subscriptions=%q", c.Paths.Subscriptions)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
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
  cache: `+filepath.Join(dir, "cache")+`
  state: `+filepath.Join(dir, "state")+`
`)
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected mkdir error, got nil")
	}
}
