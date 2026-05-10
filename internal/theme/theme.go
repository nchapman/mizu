// Package theme loads the active theme for the public site.
//
// A theme is a directory with a flat layout:
//
//	theme.yaml          # name, version, author, description, settings
//	base.liquid         # required; wraps content_for_layout
//	index.liquid        # required; homepage
//	post.liquid         # required; post + note detail
//	partials/           # optional; reusable Liquid fragments
//	assets/             # optional; served at /assets/*
//
// One default theme ships embedded in the binary. Custom themes live on
// disk at ./themes/<name>/. The active theme's FS is layered over the
// default's: any file the active theme doesn't supply falls back to the
// default. Operators can ship just the files they want to override.
package theme

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultName = "default"
	diskRoot    = "themes"
	manifest    = "theme.yaml"
)

// Required files that must resolve through the layered FS. With the
// default theme as the floor this only fails on a build bug, not an
// operator mistake.
var requiredFiles = []string{"base.liquid", "index.liquid", "post.liquid"}

// Metadata mirrors the on-disk theme.yaml. Settings here is the
// theme's *defaults*; operator overrides are merged on top in Load.
type Metadata struct {
	Name        string         `yaml:"name"`
	Version     string         `yaml:"version"`
	Author      string         `yaml:"author"`
	Description string         `yaml:"description"`
	Settings    map[string]any `yaml:"settings"`
}

// Theme is the resolved, ready-to-use bundle handed to the site server.
type Theme struct {
	Name     string         // the theme's own name from theme.yaml; falls back to the requested key
	Version  string         // from theme.yaml; "" if unset
	FS       fs.FS          // layered: active theme over the embedded default
	Settings map[string]any // merged: default-theme defaults, then active-theme defaults, then operator overrides
	Meta     Metadata       // active theme's parsed manifest (for future admin UI)
}

// Load resolves the named theme.
//
// embedded must be the binary-embedded themes/ tree (from
// //go:embed all:themes, then fs.Sub'd to "themes"). overrides are the
// operator's per-key settings overlay from config.
//
// The default theme is always loaded as the fallback floor. If name is
// "default" the active and fallback FS are the same. Otherwise we look
// for ./themes/<name>/theme.yaml on disk; missing → error that names
// both lookup paths.
func Load(name string, embedded fs.FS, overrides map[string]any) (*Theme, error) {
	if name == "" {
		name = defaultName
	}
	if embedded == nil {
		return nil, errors.New("theme: embedded FS is nil")
	}
	// Defense in depth even though Theme.Name is operator-controlled:
	// reject anything that isn't a plain directory component so a typo
	// (or a copy-pasted path) can't silently load a sibling tree.
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return nil, fmt.Errorf("theme name %q is invalid: must be a plain directory name", name)
	}

	defaultFS, defaultMeta, err := loadFromFS(embedded, defaultName)
	if err != nil {
		return nil, fmt.Errorf("load embedded default theme: %w", err)
	}

	var (
		activeFS   fs.FS
		activeMeta Metadata
	)
	if name == defaultName {
		activeFS, activeMeta = defaultFS, defaultMeta
	} else {
		diskPath := filepath.Join(diskRoot, name)
		manifestPath := filepath.Join(diskPath, manifest)
		if _, err := os.Stat(manifestPath); err != nil {
			return nil, fmt.Errorf("theme %q not found: looked in %s and embedded themes/%s", name, manifestPath, name)
		}
		// os.OpenRoot (Go 1.24+) refuses to follow symlinks that escape
		// the theme directory, even on disk filesystems. os.DirFS does
		// not — a symlink in a third-party theme's assets/ pointing at
		// /etc/passwd would otherwise be readable via /assets/<name>.
		root, err := os.OpenRoot(diskPath)
		if err != nil {
			return nil, fmt.Errorf("theme %q: open root: %w", name, err)
		}
		activeFS = root.FS()
		activeMeta, err = readManifest(activeFS)
		if err != nil {
			return nil, fmt.Errorf("theme %q: %w", name, err)
		}
	}

	layered := newLayeredFS(activeFS, defaultFS)

	for _, f := range requiredFiles {
		if _, err := fs.Stat(layered, f); err != nil {
			return nil, fmt.Errorf("theme %q: missing required file %s (active and default both lack it)", name, f)
		}
	}

	settings := mergeSettings(defaultMeta.Settings, activeMeta.Settings, overrides)

	resolvedName := activeMeta.Name
	if resolvedName == "" {
		resolvedName = name
	}

	return &Theme{
		Name:     resolvedName,
		Version:  activeMeta.Version,
		FS:       layered,
		Settings: settings,
		Meta:     activeMeta,
	}, nil
}

// loadFromFS reads <name>/theme.yaml out of the embedded tree and
// returns an FS rooted at <name> plus the parsed metadata.
func loadFromFS(root fs.FS, name string) (fs.FS, Metadata, error) {
	sub, err := fs.Sub(root, name)
	if err != nil {
		return nil, Metadata{}, fmt.Errorf("sub %q: %w", name, err)
	}
	meta, err := readManifest(sub)
	if err != nil {
		return nil, Metadata{}, fmt.Errorf("theme %q: %w", name, err)
	}
	return sub, meta, nil
}

func readManifest(themeFS fs.FS) (Metadata, error) {
	b, err := fs.ReadFile(themeFS, manifest)
	if err != nil {
		return Metadata{}, fmt.Errorf("read %s: %w", manifest, err)
	}
	var m Metadata
	if err := yaml.Unmarshal(b, &m); err != nil {
		return Metadata{}, fmt.Errorf("parse %s: %w", manifest, err)
	}
	return m, nil
}

// mergeSettings layers later maps on top of earlier ones. Nil maps are
// skipped. The result is a fresh map so callers can't mutate the
// inputs.
func mergeSettings(layers ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, l := range layers {
		for k, v := range l {
			out[k] = v
		}
	}
	return out
}
