package render

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register WebP decoder
)

// DisplayLongEdge caps the long edge of the rendered display variant.
// 1600 covers retina rendering at typical microblog content widths
// without bloating the cache. Images already smaller on both axes pass
// through unchanged; never upscaled.
const DisplayLongEdge = 1600

// JPEGQuality is the encode quality for JPEG display variants. 85 is
// the conventional sweet spot.
const JPEGQuality = 85

// ImageVariantStage produces the public display variant for every file
// in media/orig. Today this is one variant per source (matching the
// pre-render-pipeline behavior). The stage is the natural seam to add
// theme-declared widths and srcset later — its outputs would just
// expand from one per source to N.
type ImageVariantStage struct{}

func (ImageVariantStage) Name() string { return "image_variant" }

func (s ImageVariantStage) Build(_ context.Context, snap *Snapshot) ([]Output, error) {
	out := make([]Output, 0, len(snap.Media))
	var firstErr error
	for _, m := range snap.Media {
		body, name, err := s.makeVariant(m)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("variant %s: %w", m.Name, err)
			}
			continue
		}
		out = append(out, Output{Path: "media/" + name, Body: body})
	}
	return out, firstErr
}

func (ImageVariantStage) makeVariant(m MediaFile) (body []byte, displayName string, err error) {
	src, err := os.ReadFile(m.Path)
	if err != nil {
		return nil, "", err
	}
	if len(src) == 0 {
		return nil, "", fmt.Errorf("empty file")
	}
	mime := http.DetectContentType(src)

	// Drop the original extension; pick a display extension based on
	// detected type. Mirrors the per-kind rules from the old upload path.
	base := strings.TrimSuffix(m.Name, filepath.Ext(m.Name))
	switch mime {
	case "image/gif":
		// Animation preservation: pass GIFs through unchanged. Pure-Go
		// per-frame palette resizing isn't worth the complexity.
		return src, base + ".gif", nil
	case "image/png":
		body, err := encodeIfResized(src, kindPNG)
		if err != nil {
			return nil, "", err
		}
		return body, base + ".png", nil
	case "image/jpeg":
		body, err := encodeIfResized(src, kindJPEG)
		if err != nil {
			return nil, "", err
		}
		return body, base + ".jpg", nil
	case "image/webp":
		// stdlib has no WebP encoder; transcode to JPEG.
		body, err := encodeIfResized(src, kindWebP)
		if err != nil {
			return nil, "", err
		}
		return body, base + ".jpg", nil
	default:
		return nil, "", fmt.Errorf("unsupported type %s", mime)
	}
}

type imageKind int

const (
	kindPNG imageKind = iota + 1
	kindJPEG
	kindWebP
)

// encodeIfResized decodes the source, resizes if oversized, and
// re-encodes if either the resize ran or the source format isn't
// directly emittable (WebP). For already-small PNG/JPEG the original
// bytes round-trip — no quality loss, no deflate reshuffling.
func encodeIfResized(src []byte, k imageKind) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	resized := resizeIfLarger(img, DisplayLongEdge)
	if resized == img && k != kindWebP {
		return src, nil
	}
	var out bytes.Buffer
	switch k {
	case kindPNG:
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
// aspect ratio. Smaller images pass through. Pathological aspect ratios
// (e.g. 1×3200) get a 1px floor so we never write a zero-pixel canvas.
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
