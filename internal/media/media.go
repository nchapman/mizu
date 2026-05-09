// Package media handles image uploads. Files are written to a configured
// directory with a generated, content-addressable name; the original is
// preserved verbatim under orig/, and a smaller display variant is
// written alongside for embedding in posts. Type is detected from magic
// bytes — the client's Content-Type is not trusted.
package media

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register WebP decoder
)

// MaxSize caps a single upload. Generous for screenshots and photos;
// stops a runaway upload from filling disk.
const MaxSize = 10 << 20 // 10 MiB

// DisplayLongEdge is the target long-edge in pixels for the display
// variant. 1600 covers retina rendering at typical microblog content
// widths (~800px) without bloating the cache. Images already smaller
// than this on both axes are passed through, never upscaled.
const DisplayLongEdge = 1600

// JPEGQuality is the encode quality for JPEG display variants. 85 is
// the conventional sweet spot — visible artifacts only on synthetic
// gradients, ~3-5x smaller than q=100.
const JPEGQuality = 85

var (
	ErrTooLarge       = errors.New("file too large")
	ErrUnsupportedExt = errors.New("unsupported image type")
	ErrEmpty          = errors.New("empty file")
)

type kind int

const (
	kindPNG kind = iota + 1
	kindJPEG
	kindGIF
	kindWebP
)

func detect(buf []byte) (kind, error) {
	switch http.DetectContentType(buf) {
	case "image/png":
		return kindPNG, nil
	case "image/jpeg":
		return kindJPEG, nil
	case "image/gif":
		return kindGIF, nil
	case "image/webp":
		return kindWebP, nil
	default:
		return 0, fmt.Errorf("%w: %s", ErrUnsupportedExt, http.DetectContentType(buf))
	}
}

func (k kind) origExt() string {
	switch k {
	case kindPNG:
		return ".png"
	case kindJPEG:
		return ".jpg"
	case kindGIF:
		return ".gif"
	case kindWebP:
		return ".webp"
	}
	return ""
}

// displayExt is the extension of the resized variant. WebP is
// transcoded to JPEG because stdlib can't encode WebP; everything else
// keeps its source format so we don't lose PNG transparency or
// animated-GIF frames.
func (k kind) displayExt() string {
	if k == kindWebP {
		return ".jpg"
	}
	return k.origExt()
}

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
	Name string // basename of the display variant, e.g. "2026-05-08-ab12cd34.jpg"
	URL  string // public path of the display variant, e.g. "/media/2026-05-08-ab12cd34.jpg"
	Size int64  // size of the display variant on disk
	MIME string // sniffed MIME of the original upload
}

// Save reads up to MaxSize+1 bytes from r, sniffs the type, writes the
// original to <dir>/orig/<base><origExt>, and writes a resized display
// variant to <dir>/<base><displayExt>. Anything beyond MaxSize is
// rejected without keeping the partial file.
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

	k, err := detect(buf)
	if err != nil {
		return nil, err
	}
	mime := http.DetectContentType(buf)

	base, err := generateBase()
	if err != nil {
		return nil, err
	}

	// Write the original first. If display encoding fails (corrupt
	// image, etc.) we still have the source on disk for diagnosis.
	origName := base + k.origExt()
	if err := os.WriteFile(filepath.Join(s.origDir, origName), buf, 0o644); err != nil {
		return nil, err
	}

	displayBytes, err := makeDisplay(buf, k)
	if err != nil {
		// Roll back the original — a half-saved upload is worse than a
		// failed one, since the user has no way to retry the same name.
		_ = os.Remove(filepath.Join(s.origDir, origName))
		return nil, err
	}
	displayName := base + k.displayExt()
	if err := os.WriteFile(filepath.Join(s.dir, displayName), displayBytes, 0o644); err != nil {
		_ = os.Remove(filepath.Join(s.origDir, origName))
		return nil, err
	}

	return &Saved{
		Name: displayName,
		URL:  "/media/" + displayName,
		Size: int64(len(displayBytes)),
		MIME: mime,
	}, nil
}

func generateBase() (string, error) {
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("2006-01-02") + "-" + hex.EncodeToString(rnd[:]), nil
}

// makeDisplay builds the resized variant. GIFs pass through unchanged
// to preserve animation — pure-Go animated-GIF resizing requires
// per-frame palette work that's not worth the complexity for a
// microblog. Everything else is decoded, resized if oversized, and
// re-encoded.
func makeDisplay(buf []byte, k kind) ([]byte, error) {
	if k == kindGIF {
		return buf, nil
	}

	src, _, err := image.Decode(bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	resized := resizeIfLarger(src, DisplayLongEdge)

	// If no resize was needed and the format is already what we'd
	// encode to, return the original bytes — re-encoding an
	// already-small JPEG/PNG would silently degrade quality (lossy
	// generation loss for JPEG, larger files for PNG). WebP still has
	// to be transcoded since stdlib can't encode it.
	if resized == src && k != kindWebP {
		return buf, nil
	}

	var out bytes.Buffer
	switch k {
	case kindPNG:
		// Use BestSpeed: PNG output is for screenshots and line art,
		// where compression ratio between levels is small but encoding
		// time differs significantly.
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := enc.Encode(&out, resized); err != nil {
			return nil, err
		}
	case kindJPEG, kindWebP:
		if err := jpeg.Encode(&out, resized, &jpeg.Options{Quality: JPEGQuality}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unreachable: kind %d", k)
	}
	return out.Bytes(), nil
}

// resizeIfLarger scales src so its long edge equals maxEdge, preserving
// aspect ratio. Images already within bounds are returned unchanged
// (no upscaling — it would only add bytes without adding detail).
// CatmullRom is the slowest of draw's kernels but produces noticeably
// sharper photo downscales than ApproxBiLinear at the sizes we're
// targeting; we run this at most once per upload.
func resizeIfLarger(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return src
	}
	var nw, nh int
	if w >= h {
		nw = maxEdge
		nh = h * maxEdge / w
	} else {
		nh = maxEdge
		nw = w * maxEdge / h
	}
	// Pathological aspect ratios (e.g. a 1×3200 sliver) can truncate
	// the short edge to zero. NewRGBA would happily build a 0-pixel
	// canvas and Scale would write nothing — guard so we always emit
	// at least a single pixel.
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}
