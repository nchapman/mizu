package media

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makePNG returns an encoded PNG of the given dimensions filled with
// solid red. Real bytes are needed because http.DetectContentType
// looks at magic numbers; a hand-rolled header would diverge from the
// production sniff path.
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{0, 128, 255, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSave_WritesOriginalOnly(t *testing.T) {
	// Save now writes nothing but the original — display variants are
	// produced asynchronously by the render pipeline. The display dir
	// (s.dir) should contain only the orig/ subdirectory after a Save.
	s := newStore(t)
	src := makePNG(t, 200, 100)
	got, err := s.Save(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got.Name, ".png") {
		t.Errorf("name=%q, want .png suffix", got.Name)
	}
	if got.MIME != "image/png" {
		t.Errorf("MIME=%q", got.MIME)
	}
	orig, err := os.ReadFile(filepath.Join(s.origDir, got.Name))
	if err != nil {
		t.Fatalf("original missing: %v", err)
	}
	if !bytes.Equal(orig, src) {
		t.Error("original bytes differ from input")
	}
	// No display variant should have been written into s.dir.
	entries, _ := os.ReadDir(s.dir)
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("Save wrote unexpected file %s — display variants must come from the render pipeline", e.Name())
		}
	}
}

func TestSave_URLPointsAtDisplayVariant(t *testing.T) {
	// WebP transcodes to JPEG in the display variant; the URL Save
	// returns must reflect that so the admin UI embeds the right path.
	// Use a JPEG → JPEG case here since testing without WebP fixture
	// and the rule still demonstrates the URL/extension contract.
	s := newStore(t)
	got, err := s.Save(bytes.NewReader(makeJPEG(t, 50, 50)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.URL, "/media/") {
		t.Errorf("URL=%q, want /media/ prefix", got.URL)
	}
	if !strings.HasSuffix(got.URL, ".jpg") {
		t.Errorf("URL=%q, want .jpg suffix for JPEG upload", got.URL)
	}
}

func TestSave_RejectsNonImage(t *testing.T) {
	s := newStore(t)
	_, err := s.Save(strings.NewReader("hello world this is plain text"))
	if !errors.Is(err, ErrUnsupportedExt) {
		t.Errorf("err=%v want ErrUnsupportedExt", err)
	}
	for _, d := range []string{s.dir, s.origDir} {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if !e.IsDir() {
				t.Errorf("unexpected file in %s: %s", d, e.Name())
			}
		}
	}
}

func TestSave_RejectsEmpty(t *testing.T) {
	s := newStore(t)
	_, err := s.Save(strings.NewReader(""))
	if !errors.Is(err, ErrEmpty) {
		t.Errorf("err=%v want ErrEmpty", err)
	}
}

func TestSave_RejectsTooLarge(t *testing.T) {
	s := newStore(t)
	big := append(makePNG(t, 10, 10), bytes.Repeat([]byte{0}, MaxSize+1)...)
	_, err := s.Save(bytes.NewReader(big))
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("err=%v want ErrTooLarge", err)
	}
}

func TestSave_UniqueNames(t *testing.T) {
	s := newStore(t)
	src := makePNG(t, 50, 50)
	a, err := s.Save(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Save(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if a.Name == b.Name {
		t.Errorf("two saves produced same name %q", a.Name)
	}
}
