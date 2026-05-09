package media

import (
	"bytes"
	"errors"
	"image"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "golang.org/x/image/webp"
)

// minimalWebP is a 4x4 lossy WebP. Stdlib has no WebP encoder, so the
// bytes were generated once via `cwebp` from a flat PNG and inlined.
var minimalWebP = []byte{
	0x52, 0x49, 0x46, 0x46, 0x38, 0x00, 0x00, 0x00,
	0x57, 0x45, 0x42, 0x50, 0x56, 0x50, 0x38, 0x20,
	0x2C, 0x00, 0x00, 0x00, 0xF0, 0x01, 0x00, 0x9D,
	0x01, 0x2A, 0x04, 0x00, 0x04, 0x00, 0x02, 0x00,
	0x34, 0x25, 0xA0, 0x02, 0x74, 0xBA, 0x01, 0xF8,
	0x00, 0x04, 0xC8, 0x00, 0x00, 0xFE, 0x97, 0x17,
	0xFF, 0x20, 0xB9, 0x61, 0x75, 0xC8, 0xD7, 0xFE,
	0x71, 0x2A, 0x20, 0x93, 0x79, 0x80, 0x00, 0x00,
}

func TestSave_WebPTranscodedToJPEG(t *testing.T) {
	if got := http.DetectContentType(minimalWebP); got != "image/webp" {
		t.Fatalf("inlined fixture not detected as WebP: %s", got)
	}
	s := newStore(t)
	saved, err := s.Save(bytes.NewReader(minimalWebP))
	if err != nil {
		t.Fatalf("Save WebP: %v", err)
	}
	if !strings.HasSuffix(saved.Name, ".jpg") {
		t.Errorf("display name=%q, want .jpg suffix", saved.Name)
	}
	if saved.MIME != "image/webp" {
		t.Errorf("MIME=%q, want image/webp (sniff of original)", saved.MIME)
	}
	// Display variant should be a real JPEG.
	disp, err := os.ReadFile(filepath.Join(s.dir, saved.Name))
	if err != nil {
		t.Fatal(err)
	}
	if http.DetectContentType(disp) != "image/jpeg" {
		t.Errorf("display sniff=%q, want image/jpeg", http.DetectContentType(disp))
	}
	if _, _, err := image.Decode(bytes.NewReader(disp)); err != nil {
		t.Errorf("display variant not decodable: %v", err)
	}
	// Original kept verbatim under orig/. List the dir rather than
	// reconstructing the filename — keeps the test independent of the
	// orig/display extension mapping.
	entries, err := os.ReadDir(s.origDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("orig dir has %d files, want 1", len(entries))
	}
	orig, err := os.ReadFile(filepath.Join(s.origDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("orig WebP: %v", err)
	}
	if !bytes.Equal(orig, minimalWebP) {
		t.Error("WebP original not preserved verbatim")
	}
}

func TestSave_RejectsSVG(t *testing.T) {
	// SVG is sniffed as text/xml (or text/plain), not an image/* — must
	// be rejected to avoid the XSS surface of allowing inline scripts in
	// image embeds.
	svg := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"></svg>`)
	s := newStore(t)
	_, err := s.Save(bytes.NewReader(svg))
	if !errors.Is(err, ErrUnsupportedExt) {
		t.Errorf("err=%v, want ErrUnsupportedExt", err)
	}
}

func TestSave_NoLeftoversOnDisplayFailure(t *testing.T) {
	// A file whose magic bytes claim PNG but whose body is invalid will
	// pass detect() and fail decode(). The original must be rolled back
	// so we don't have a half-saved upload.
	bogus := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0xFF}, 64)...)
	s := newStore(t)
	_, err := s.Save(bytes.NewReader(bogus))
	if err == nil {
		t.Fatal("expected decode error for malformed PNG")
	}
	for _, d := range []string{s.dir, s.origDir} {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if !e.IsDir() {
				t.Errorf("leftover file in %s after failed save: %s", d, e.Name())
			}
		}
	}
}
