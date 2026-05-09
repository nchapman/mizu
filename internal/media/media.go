// Package media handles image uploads. Files are written to a configured
// directory with a generated, content-addressable name. Type is detected
// from magic bytes — the client's Content-Type is not trusted.
package media

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// MaxSize caps a single upload. Generous for screenshots and photos;
// stops a runaway upload from filling disk.
const MaxSize = 10 << 20 // 10 MiB

var (
	ErrTooLarge       = errors.New("file too large")
	ErrUnsupportedExt = errors.New("unsupported image type")
	ErrEmpty          = errors.New("empty file")
)

// extByMIME maps the MIME types http.DetectContentType produces for
// images to the canonical extension we store on disk. We use the
// detected type — not the upload's Content-Type or filename — as the
// source of truth so a renamed .exe can't masquerade as a .png.
//
// SVG is intentionally excluded: it can carry inline <script>, and
// http.DetectContentType doesn't sniff it as image/svg+xml anyway
// (the spec doesn't include SVG).
var extByMIME = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

type Store struct {
	dir string
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

type Saved struct {
	Name string // basename on disk, e.g. "2026-05-08-ab12cd34.png"
	URL  string // public path, e.g. "/media/2026-05-08-ab12cd34.png"
	Size int64
	MIME string
}

// Save reads up to MaxSize+1 bytes from r, sniffs the type, and writes
// it under a generated filename. Anything beyond MaxSize is rejected
// without keeping the partial file.
func (s *Store) Save(r io.Reader) (*Saved, error) {
	buf, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(buf) == 0 {
		return nil, ErrEmpty
	}
	if int64(len(buf)) > MaxSize {
		return nil, ErrTooLarge
	}

	mime := http.DetectContentType(buf)
	ext, ok := extByMIME[mime]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedExt, mime)
	}

	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, err
	}
	name := time.Now().UTC().Format("2006-01-02") + "-" + hex.EncodeToString(rnd[:]) + ext
	full := filepath.Join(s.dir, name)

	if err := os.WriteFile(full, buf, 0o644); err != nil {
		return nil, err
	}
	return &Saved{Name: name, URL: "/media/" + name, Size: int64(len(buf)), MIME: mime}, nil
}
