package theme

import (
	"io/fs"
)

// layeredFS resolves names against the active FS first and falls back
// to the default FS for anything missing. This is what lets a custom
// theme ship just `index.liquid` and inherit `base.liquid`,
// partials/*, and assets/style.css from the default.
//
// The implementation is intentionally minimal — it covers the operations
// fs.ReadFile, http.FileServer, and liquid.FileSystemLoader actually
// invoke (Open + ReadFile + Stat). It does not attempt to merge
// directory listings: ReadDir on a path returns whichever FS responds
// successfully, active-first. We don't enumerate themed directories
// anywhere in the codebase, so this is fine for now.
type layeredFS struct {
	active   fs.FS
	fallback fs.FS
}

func newLayeredFS(active, fallback fs.FS) fs.FS {
	if active == nil {
		return fallback
	}
	if fallback == nil || active == fallback {
		return active
	}
	return &layeredFS{active: active, fallback: fallback}
}

func (l *layeredFS) Open(name string) (fs.File, error) {
	f, err := l.active.Open(name)
	if err == nil {
		return f, nil
	}
	return l.fallback.Open(name)
}

func (l *layeredFS) ReadFile(name string) ([]byte, error) {
	if b, err := fs.ReadFile(l.active, name); err == nil {
		return b, nil
	}
	return fs.ReadFile(l.fallback, name)
}

func (l *layeredFS) Stat(name string) (fs.FileInfo, error) {
	if fi, err := fs.Stat(l.active, name); err == nil {
		return fi, nil
	}
	return fs.Stat(l.fallback, name)
}

func (l *layeredFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if entries, err := fs.ReadDir(l.active, name); err == nil {
		return entries, nil
	}
	return fs.ReadDir(l.fallback, name)
}
