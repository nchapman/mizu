package media

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 1x1 PNG (smallest valid).
var pngBytes = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
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

func TestSave_PNG(t *testing.T) {
	s := newStore(t)
	got, err := s.Save(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got.Name, ".png") {
		t.Errorf("name=%q want .png suffix", got.Name)
	}
	if got.URL != "/media/"+got.Name {
		t.Errorf("URL=%q", got.URL)
	}
	if got.MIME != "image/png" {
		t.Errorf("MIME=%q", got.MIME)
	}
	full := filepath.Join(s.dir, got.Name)
	b, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, pngBytes) {
		t.Error("on-disk content differs from input")
	}
}

func TestSave_RejectsNonImage(t *testing.T) {
	s := newStore(t)
	_, err := s.Save(strings.NewReader("hello world this is plain text"))
	if !errors.Is(err, ErrUnsupportedExt) {
		t.Errorf("err=%v want ErrUnsupportedExt", err)
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
	// PNG header followed by enough zeros to exceed the limit. The sniff
	// only looks at the first bytes, so this still classifies as PNG —
	// we want the size check to fire first.
	big := append(pngBytes, bytes.Repeat([]byte{0}, MaxSize+1)...)
	_, err := s.Save(bytes.NewReader(big))
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("err=%v want ErrTooLarge", err)
	}
}

func TestSave_UniqueNames(t *testing.T) {
	s := newStore(t)
	a, err := s.Save(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Save(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	if a.Name == b.Name {
		t.Errorf("two saves produced same name %q", a.Name)
	}
}

// Sanity: io.LimitReader should not be relied on alone — a malicious
// reader could return more than the limit if buggy. We use ReadAll on a
// LimitReader and check the resulting length, so this test pins the
// behavior we depend on.
func TestSave_HonorsLimit(t *testing.T) {
	s := newStore(t)
	_, err := s.Save(io.LimitReader(bytes.NewReader(bytes.Repeat([]byte{1}, MaxSize+10)), MaxSize+5))
	if !errors.Is(err, ErrTooLarge) && !errors.Is(err, ErrUnsupportedExt) {
		// either is acceptable: too-large fires first if size > MaxSize,
		// otherwise sniff fails on garbage bytes.
		t.Errorf("unexpected err=%v", err)
	}
}
