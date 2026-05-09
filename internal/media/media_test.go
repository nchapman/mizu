package media

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "golang.org/x/image/webp"
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

func makeGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	pal := color.Palette{color.Black, color.White}
	img := image.NewPaletted(image.Rect(0, 0, w, h), pal)
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
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

func decodeBounds(t *testing.T, b []byte) image.Rectangle {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return img.Bounds()
}

func TestSave_PNG_PassthroughBelowLimit(t *testing.T) {
	s := newStore(t)
	src := makePNG(t, 200, 100)
	got, err := s.Save(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got.Name, ".png") {
		t.Errorf("display ext=%q, want .png", filepath.Ext(got.Name))
	}
	if got.MIME != "image/png" {
		t.Errorf("MIME=%q", got.MIME)
	}

	// Original preserved byte-for-byte.
	origBase := strings.TrimSuffix(got.Name, ".png") + ".png"
	orig, err := os.ReadFile(filepath.Join(s.origDir, origBase))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(orig, src) {
		t.Error("original bytes differ from input")
	}

	// Display variant is still 200x100 (no upscale, no downscale).
	disp, err := os.ReadFile(filepath.Join(s.dir, got.Name))
	if err != nil {
		t.Fatal(err)
	}
	b := decodeBounds(t, disp)
	if b.Dx() != 200 || b.Dy() != 100 {
		t.Errorf("display bounds=%v, want 200x100", b)
	}
}

func TestSave_PNG_ResizesOversized(t *testing.T) {
	s := newStore(t)
	// 4000x2000 — long edge well over 1600.
	src := makePNG(t, 4000, 2000)
	got, err := s.Save(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	disp, err := os.ReadFile(filepath.Join(s.dir, got.Name))
	if err != nil {
		t.Fatal(err)
	}
	b := decodeBounds(t, disp)
	if b.Dx() != DisplayLongEdge {
		t.Errorf("display width=%d, want %d", b.Dx(), DisplayLongEdge)
	}
	if b.Dy() != 800 {
		// 4000x2000 → 1600x800 (aspect preserved)
		t.Errorf("display height=%d, want 800", b.Dy())
	}
}

func TestSave_JPEG_TallerThanWide(t *testing.T) {
	s := newStore(t)
	// 1000x3200 — long edge is height.
	src := makeJPEG(t, 1000, 3200)
	got, err := s.Save(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	disp, err := os.ReadFile(filepath.Join(s.dir, got.Name))
	if err != nil {
		t.Fatal(err)
	}
	b := decodeBounds(t, disp)
	if b.Dy() != DisplayLongEdge {
		t.Errorf("display height=%d, want %d", b.Dy(), DisplayLongEdge)
	}
	if b.Dx() != 500 {
		// 1000x3200 → 500x1600
		t.Errorf("display width=%d, want 500", b.Dx())
	}
}

func TestSave_GIF_Passthrough(t *testing.T) {
	s := newStore(t)
	// Oversized GIF: would normally trigger resize, but GIFs pass through.
	src := makeGIF(t, 2400, 1200)
	got, err := s.Save(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got.Name, ".gif") {
		t.Errorf("display ext=%q, want .gif", filepath.Ext(got.Name))
	}
	disp, err := os.ReadFile(filepath.Join(s.dir, got.Name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(disp, src) {
		t.Error("GIF display variant differs from source — should be passthrough")
	}
}

func TestSave_RejectsNonImage(t *testing.T) {
	s := newStore(t)
	_, err := s.Save(strings.NewReader("hello world this is plain text"))
	if !errors.Is(err, ErrUnsupportedExt) {
		t.Errorf("err=%v want ErrUnsupportedExt", err)
	}
	// Make sure nothing was written to either dir.
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

func TestSave_PNG_NoReencodeWhenSmall(t *testing.T) {
	// A small PNG should be passed through to the display dir
	// byte-for-byte — re-encoding would either lose quality or just
	// shuffle the same pixels through a different deflate stream.
	s := newStore(t)
	src := makePNG(t, 200, 100)
	got, err := s.Save(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	disp, err := os.ReadFile(filepath.Join(s.dir, got.Name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(disp, src) {
		t.Error("small PNG was re-encoded; expected byte-for-byte passthrough")
	}
}

func TestResizeIfLarger_DegenerateAspectRatio(t *testing.T) {
	// A 1×3200 sliver scaled to a 1600 long-edge would compute width=0
	// without the floor guard, producing a zero-pixel canvas.
	src := image.NewRGBA(image.Rect(0, 0, 1, 3200))
	got := resizeIfLarger(src, 1600)
	b := got.Bounds()
	if b.Dx() < 1 || b.Dy() < 1 {
		t.Errorf("got %dx%d, want both dims >= 1", b.Dx(), b.Dy())
	}
}

func TestResizeIfLarger_NoUpscale(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 100, 50))
	got := resizeIfLarger(src, 1600)
	if got != image.Image(src) {
		t.Error("small image was modified; expected passthrough (no upscale)")
	}
}
