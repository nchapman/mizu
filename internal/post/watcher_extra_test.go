package post

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWatcher_DebouncesBurst writes several .md files in quick
// succession and confirms all of them end up in the store. Without a
// working debounce loop a torn reload could miss whichever file was
// being written when Reload started.
func TestWatcher_DebouncesBurst(t *testing.T) {
	s := newStore(t)
	w := NewWatcher(s)
	w.debounce = 30 * time.Millisecond
	w.ready = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()
	<-w.ready

	postsDir, _ := s.Dirs()
	const n = 5
	for i := 0; i < n; i++ {
		raw := fmt.Sprintf("---\nid: burst%d\ntitle: T%d\nslug: t%d\ndate: 2024-01-0%dT00:00:00Z\n---\n\nbody\n", i, i, i, i+1)
		path := filepath.Join(postsDir, fmt.Sprintf("2024-01-0%d-t%d-burst%d.md", i+1, i, i))
		if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if !waitFor(t, 2*time.Second, func() bool {
		for i := 0; i < n; i++ {
			if _, ok := s.ByID(fmt.Sprintf("burst%d", i)); !ok {
				return false
			}
		}
		return true
	}) {
		t.Fatal("watcher didn't reload all burst writes within deadline")
	}
}
