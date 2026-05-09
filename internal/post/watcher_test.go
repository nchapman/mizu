package post

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitFor polls fn until it returns true or the deadline passes. Used
// to wait on the watcher's debounced reload without sleeping for the
// worst-case fixed duration.
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestWatcher_PicksUpExternalWrites(t *testing.T) {
	s := newStore(t)
	w := NewWatcher(s)
	w.debounce = 30 * time.Millisecond // tighten so the test isn't slow
	w.ready = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-w.ready

	postsDir, _ := s.Dirs()
	raw := `---
id: ext1
title: External
slug: external
date: 2024-01-01T00:00:00Z
---

written by hand
`
	path := filepath.Join(postsDir, "2024-01-01-external-ext1.md")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	if !waitFor(t, 2*time.Second, func() bool {
		_, ok := s.ByID("ext1")
		return ok
	}) {
		t.Fatal("watcher never picked up the external write")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("Run didn't exit after cancel")
	}
}

func TestWatcher_IgnoresNonMarkdownFiles(t *testing.T) {
	s := newStore(t)
	w := NewWatcher(s)
	w.debounce = 30 * time.Millisecond
	w.ready = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()
	<-w.ready

	postsDir, _ := s.Dirs()
	// Editor swap file. The watcher should silently drop this without
	// reloading — there's no good way to verify "didn't reload"
	// directly, so we instead verify a subsequent .md write still
	// triggers a reload, i.e. the swap event didn't break anything.
	if err := os.WriteFile(filepath.Join(postsDir, ".garbage.md.swp"), []byte("vim"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond) // > debounce

	raw := "---\nid: real1\ntitle: Real\nslug: real\ndate: 2024-01-01T00:00:00Z\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(postsDir, "2024-01-01-real-real.md"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 2*time.Second, func() bool {
		_, ok := s.ByID("real1")
		return ok
	}) {
		t.Fatal("watcher did not reload after a real .md write")
	}
}
