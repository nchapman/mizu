package theme

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

// minimalEmbedded mirrors the shape of the binary-embedded themes/
// tree: a single "default" entry with the required files plus a
// partial and an asset, so the layered FS has something to shadow.
func minimalEmbedded() fstest.MapFS {
	return fstest.MapFS{
		"default/theme.yaml": &fstest.MapFile{Data: []byte(`name: Default
version: "1"
settings:
  accent_color: "#0066cc"
  max_width: "42rem"
`)},
		"default/base.liquid":            &fstest.MapFile{Data: []byte("BASE:{{ content_for_layout }}")},
		"default/index.liquid":           &fstest.MapFile{Data: []byte("INDEX")},
		"default/post.liquid":            &fstest.MapFile{Data: []byte("POST")},
		"default/partials/header.liquid": &fstest.MapFile{Data: []byte("HEADER")},
		"default/assets/style.css":       &fstest.MapFile{Data: []byte("body{}")},
	}
}

func TestLoad_DefaultThemeFromEmbedded(t *testing.T) {
	t.Chdir(t.TempDir()) // ensure no stray ./themes/ on disk

	th, err := Load("default", minimalEmbedded(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if th.Name != "Default" {
		t.Errorf("Name = %q, want %q (from theme.yaml)", th.Name, "Default")
	}
	if th.Version != "1" {
		t.Errorf("Version = %q, want %q", th.Version, "1")
	}
	if got := th.Settings["accent_color"]; got != "#0066cc" {
		t.Errorf("accent_color default not loaded: got %v", got)
	}
	for _, f := range []string{"base.liquid", "index.liquid", "post.liquid", "partials/header.liquid", "assets/style.css"} {
		if _, err := th.FS.Open(f); err != nil {
			t.Errorf("FS.Open(%q): %v", f, err)
		}
	}
}

func TestLoad_EmptyNameDefaults(t *testing.T) {
	t.Chdir(t.TempDir())
	th, err := Load("", minimalEmbedded(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if th.Name != "Default" {
		t.Errorf("empty name didn't default: got %q", th.Name)
	}
}

func TestLoad_DiskThemeOverlaysDefault(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// Custom theme ships only theme.yaml + index.liquid. base.liquid,
	// post.liquid, partials/, and assets/ must come from the default.
	customDir := filepath.Join(dir, "themes", "custom")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "theme.yaml"), []byte(`name: Custom
version: "2"
settings:
  accent_color: "#ff0066"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "index.liquid"), []byte("CUSTOM_INDEX"), 0o644); err != nil {
		t.Fatal(err)
	}

	th, err := Load("custom", minimalEmbedded(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if th.Name != "Custom" {
		t.Errorf("Name = %q, want %q", th.Name, "Custom")
	}
	idx, err := readAll(th, "index.liquid")
	if err != nil {
		t.Fatalf("read index.liquid: %v", err)
	}
	if idx != "CUSTOM_INDEX" {
		t.Errorf("active theme index not used: %q", idx)
	}
	base, err := readAll(th, "base.liquid")
	if err != nil {
		t.Fatalf("read base.liquid: %v", err)
	}
	if !strings.Contains(base, "BASE") {
		t.Errorf("base.liquid did not fall back to default: %q", base)
	}
	// Default's partial reachable through the layered FS, so the custom
	// theme's index.liquid could `{% render "partials/header" %}`.
	header, err := readAll(th, "partials/header.liquid")
	if err != nil {
		t.Fatalf("read partial: %v", err)
	}
	if header != "HEADER" {
		t.Errorf("partial fallback: %q", header)
	}
}

func TestLoad_SettingsPrecedence(t *testing.T) {
	// Operator override wins over both default-theme defaults and the
	// active-theme defaults. Active-theme defaults win over default
	// defaults. Keys not present in the active theme still fall through
	// to the default theme's defaults.
	dir := t.TempDir()
	t.Chdir(dir)
	customDir := filepath.Join(dir, "themes", "custom")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "theme.yaml"), []byte(`name: Custom
version: "1"
settings:
  accent_color: "#222222"
  custom_only: "x"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The custom theme has no liquid files of its own; everything falls
	// back to the default. That's enough to satisfy required-file checks.

	overrides := map[string]any{"accent_color": "#ff0066"}
	th, err := Load("custom", minimalEmbedded(), overrides)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := th.Settings["accent_color"]; got != "#ff0066" {
		t.Errorf("override didn't win: %v", got)
	}
	if got := th.Settings["max_width"]; got != "42rem" {
		t.Errorf("default-theme default didn't fall through: %v", got)
	}
	if got := th.Settings["custom_only"]; got != "x" {
		t.Errorf("active-theme own setting missing: %v", got)
	}
}

func TestLoad_MissingThemeNamesBothPaths(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := Load("nope", minimalEmbedded(), nil)
	if err == nil {
		t.Fatal("expected error for missing theme")
	}
	msg := err.Error()
	if !strings.Contains(msg, "themes/nope/theme.yaml") {
		t.Errorf("error should name disk path: %v", err)
	}
	if !strings.Contains(msg, "embedded themes/nope") {
		t.Errorf("error should name embedded path: %v", err)
	}
}

func TestLoad_NilEmbeddedRejected(t *testing.T) {
	if _, err := Load("default", nil, nil); err == nil {
		t.Error("nil embedded FS should error")
	}
}

func readAll(th *Theme, name string) (string, error) {
	f, err := th.FS.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	return string(b), err
}

func TestLoad_RejectsInvalidThemeNames(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, bad := range []string{"../etc", "foo/bar", `foo\bar`, ".", ".."} {
		if _, err := Load(bad, minimalEmbedded(), nil); err == nil {
			t.Errorf("Load(%q) should have errored", bad)
		}
	}
}

// TestLoad_DiskThemeRefusesSymlinkEscape pins VULN-01 from review:
// os.OpenRoot must refuse to follow a symlink that escapes the theme
// directory, even though os.DirFS would happily serve its contents.
func TestLoad_DiskThemeRefusesSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	dir := t.TempDir()
	t.Chdir(dir)

	// Secret outside the theme tree. A vulnerable loader would happily
	// serve this through the layered FS.
	secret := filepath.Join(dir, "outside-secret")
	if err := os.WriteFile(secret, []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}

	customDir := filepath.Join(dir, "themes", "custom")
	if err := os.MkdirAll(filepath.Join(customDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "theme.yaml"), []byte("name: Custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(customDir, "assets", "leak")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	th, err := Load("custom", minimalEmbedded(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := th.FS.Open("assets/leak"); err == nil {
		t.Error("symlink that escapes the theme root should not be readable")
	}
}
