package render

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

// Watcher subscribes to every source directory the pipeline reads —
// posts/, drafts/, media/orig/, and the active theme on disk if one is
// configured — and pulses Pipeline.Enqueue on every relevant change.
//
// Debouncing is the pipeline's responsibility (its Run loop owns the
// debounce timer). The watcher's only job is "something changed,
// kick the queue."
type Watcher struct {
	pipeline *Pipeline
	dirs     []string
	files    []string // absolute or relative file paths to watch

	// fileBasenames maps each watched file's *parent directory* to the
	// set of basenames that should fire a build. Populated in Run.
	// Watching the parent dir (rather than the file directly) is
	// required because inotify/kqueue watches the inode — an editor
	// that saves via temp+rename invalidates a file-level watch
	// silently.
	fileBasenames map[string]map[string]bool

	// ready is closed once every fsnotify subscription is registered.
	// Tests use it to avoid racing the first write.
	ready chan struct{}
}

// NewWatcher returns a Watcher. dirs is the set of directories to
// recursively watch; files is the set of individual file paths to react
// to (used for config.yaml, which doesn't live in a directory we want
// to watch wholesale). Missing entries are silently skipped — the
// drafts directory may not exist on a fresh install; an embedded theme
// has no disk path.
func NewWatcher(p *Pipeline, dirs []string, files []string) *Watcher {
	cleanDirs := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d == "" {
			continue
		}
		cleanDirs = append(cleanDirs, d)
	}
	cleanFiles := make([]string, 0, len(files))
	for _, f := range files {
		if f == "" {
			continue
		}
		cleanFiles = append(cleanFiles, f)
	}
	return &Watcher{pipeline: p, dirs: cleanDirs, files: cleanFiles}
}

// Run blocks until ctx is cancelled. fsnotify errors are logged and the
// watcher continues — only failures to register subscriptions return
// from Run.
func (w *Watcher) Run(ctx context.Context) error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fw.Close()

	for _, d := range w.dirs {
		if err := addRecursive(fw, d); err != nil {
			return err
		}
	}
	// Files: watch the *parent directory* and filter events by basename.
	// fw.Add(file) attaches to the inode, which an atomic-rename editor
	// invalidates on the first save. Watching the parent dir survives
	// any rename behavior.
	w.fileBasenames = map[string]map[string]bool{}
	for _, f := range w.files {
		parent := filepath.Dir(f)
		if _, err := os.Stat(parent); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				log.Printf("render watcher: parent of %q does not exist; will miss changes until restart", f)
				continue
			}
			return err
		}
		if err := fw.Add(parent); err != nil {
			return err
		}
		set := w.fileBasenames[parent]
		if set == nil {
			set = map[string]bool{}
			w.fileBasenames[parent] = set
		}
		set[filepath.Base(f)] = true
	}
	if w.ready != nil {
		close(w.ready)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-fw.Events:
			if !ok {
				return nil
			}
			if !relevantEvent(ev) {
				continue
			}
			// If the event happened in a directory we watch only for
			// specific filenames (e.g. the dir containing config.yaml),
			// drop events for unrelated files in the same dir.
			if set, watched := w.fileBasenames[filepath.Dir(ev.Name)]; watched {
				if !set[filepath.Base(ev.Name)] {
					continue
				}
			}
			// New subdirectory inside a watched root: subscribe so
			// future writes inside it are seen. Enqueue immediately
			// in case files were created inside the new dir *before*
			// our watch landed — a race during e.g. `mkdir foo && cp -r src/* foo/`.
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					_ = addRecursive(fw, ev.Name)
				}
			}
			w.pipeline.Enqueue()
		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			log.Printf("render watcher: %v", err)
		}
	}
}

// addRecursive walks dir and adds every directory inside it to fw.
// Missing dirs are not an error; an embedded-theme deployment doesn't
// have a themes/ directory on disk to watch.
func addRecursive(fw *fsnotify.Watcher, dir string) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		if err := fw.Add(path); err != nil {
			return err
		}
		return nil
	})
}

// relevantEvent filters out noise — editor swap files, lock files,
// our own temp files. Anything that isn't a real source change.
func relevantEvent(ev fsnotify.Event) bool {
	base := filepath.Base(ev.Name)
	switch {
	case strings.HasSuffix(base, "~"):
		return false
	case strings.HasPrefix(base, ".#"): // emacs lock
		return false
	case strings.HasSuffix(base, ".swp"), strings.HasSuffix(base, ".swx"):
		return false
	case strings.Contains(base, ".tmp-"): // our own atomicWrite tempfiles
		return false
	}
	return true
}
