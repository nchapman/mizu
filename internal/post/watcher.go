package post

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher reloads the post store when markdown files in posts/ or
// drafts/ change on disk. The intent is to support out-of-band edits
// — running `vim content/posts/foo.md` should make changes show up
// without restarting repeat.
//
// Reloads are debounced because typical editor saves emit a burst of
// events (rename, create, write) for a single user action; reloading
// once at the end is cheap and avoids torn re-reads of half-written
// files.
type Watcher struct {
	store    *Store
	debounce time.Duration

	// ready, if non-nil, is closed once fsnotify has registered
	// every directory subscription. Tests use it to avoid racing
	// the writes against the watcher's own setup.
	ready chan struct{}
}

// NewWatcher returns a Watcher that reloads s when its directories
// change. 200 ms is enough to coalesce an editor's atomic-rename
// flurry without making changes feel slow.
func NewWatcher(s *Store) *Watcher {
	return &Watcher{store: s, debounce: 200 * time.Millisecond}
}

// Run watches the store's directories until ctx is cancelled. Errors
// from the watcher itself (transient I/O issues) are logged and
// processing continues; only failures to construct the watcher or
// register subscriptions return an error.
//
// Concurrency note: the debounce is implemented as a single goroutine
// that owns its own timer channel — there is no shared timer to Reset,
// which sidesteps the documented AfterFunc/Reset race where a queued
// fire can run concurrently with a reschedule.
func (w *Watcher) Run(ctx context.Context) error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fw.Close()

	postsDir, draftsDir := w.store.Dirs()
	for _, dir := range []string{postsDir, draftsDir} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(dir); err != nil {
			// Drafts directory may not exist yet on a fresh install
			// — that's fine, we'll skip it. The store handles
			// missing draftDir on reload too.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if err := fw.Add(dir); err != nil {
			return err
		}
	}
	if w.ready != nil {
		close(w.ready)
	}

	// pulses signal "an event happened, (re)start the debounce."
	// Buffered to 1 so a burst of events coalesces without blocking
	// the fsnotify reader; we only need to know that *something*
	// happened, not how many things.
	pulses := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-fw.Events:
				if !ok {
					return
				}
				// Editors create .swp, .tmp, lock files, etc. that
				// don't represent post content; firing on them would
				// just churn.
				if !strings.HasSuffix(ev.Name, ".md") {
					continue
				}
				select {
				case pulses <- struct{}{}:
				default: // already signalled
				}
			case err, ok := <-fw.Errors:
				if !ok {
					return
				}
				log.Printf("post watcher: %v", err)
			}
		}
	}()

	// Debounce loop owned by a single goroutine — no shared state,
	// so no race-prone Reset gymnastics. Each pulse replaces our
	// view of the active timer; older timers fire harmlessly into
	// channels we've stopped selecting on.
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pulses:
			timerC = time.After(w.debounce)
		case <-timerC:
			timerC = nil
			if err := w.store.Reload(); err != nil {
				log.Printf("post reload: %v", err)
			}
		}
	}
}
