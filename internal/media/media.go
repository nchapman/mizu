// Package media accepts image uploads. The handler validates type by
// magic bytes, caps size, and writes the original to <dir>/orig — and
// nothing else. Display variants are produced by the render pipeline's
// ImageVariantStage when it sees the file appear under media/orig.
//
// The upload path used to decode/resize/encode synchronously. That
// stage moved into the render pipeline so input writes raw bytes and
// the pipeline owns every derivation. The trade-off: a brand-new
// upload appears under /media/<name> after one render cycle (sub-
// second in steady state) instead of synchronously. Upstream callers
// (the admin upload form) should not assume the public URL resolves
// the instant Save returns.
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

// Store writes uploaded originals to <dir>/orig. The render pipeline
// reads from there to produce display variants under <publicDir>/media.
type Store struct {
	dir     string
	origDir string
}

func NewStore(dir string) (*Store, error) {
	origDir := filepath.Join(dir, "orig")
	if err := os.MkdirAll(origDir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir, origDir: origDir}, nil
}

type Saved struct {
	Name string // basename of the file (with original extension), e.g. "2026-05-08-ab12cd34.png"
	URL  string // public path the variant will be served at after the next render
	Size int64  // bytes written
	MIME string // sniffed MIME of the upload
}

// Save reads up to MaxSize+1 bytes from r, sniffs the type, writes the
// original to <dir>/orig/<base><ext>, and returns the public URL the
// display variant will live at after the render pipeline picks it up.
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
	ext, displayExt, ok := classify(mime)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedExt, mime)
	}

	base, err := generateBase()
	if err != nil {
		return nil, err
	}
	origName := base + ext
	if err := os.WriteFile(filepath.Join(s.origDir, origName), buf, 0o644); err != nil {
		return nil, err
	}
	return &Saved{
		Name: origName,
		// The render pipeline produces /media/<base><displayExt>; that's
		// the URL the admin UI should use when embedding the upload.
		URL:  "/media/" + base + displayExt,
		Size: int64(len(buf)),
		MIME: mime,
	}, nil
}

// classify maps a sniffed MIME to (origExt, displayExt). The display
// extension reflects what the render pipeline will emit: WebP gets
// transcoded to JPEG, everything else keeps its source format.
func classify(mime string) (origExt, displayExt string, ok bool) {
	switch mime {
	case "image/png":
		return ".png", ".png", true
	case "image/jpeg":
		return ".jpg", ".jpg", true
	case "image/gif":
		return ".gif", ".gif", true
	case "image/webp":
		return ".webp", ".jpg", true
	}
	return "", "", false
}

func generateBase() (string, error) {
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("2006-01-02") + "-" + hex.EncodeToString(rnd[:]), nil
}
