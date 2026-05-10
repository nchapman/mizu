package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/nchapman/mizu/internal/config"
	"github.com/nchapman/mizu/internal/post"
	"github.com/nchapman/mizu/internal/theme"
	"github.com/nchapman/mizu/internal/webmention"
)

// MediaFile is a single original under media/orig that the
// ImageVariantStage will derive a display variant from.
type MediaFile struct {
	Name string // basename, e.g. "2026-05-08-ab12cd34.png"
	Path string // absolute path on disk
	Size int64
}

// MentionView is the post-page-friendly shape of a verified mention.
// Built once per snapshot so each post page render doesn't re-hit SQLite.
type MentionView struct {
	Source     string
	VerifiedAt int64 // unix seconds; templates need a time, see post_page stage
}

// Snapshot is a read-only view of every source the stages need to
// render. Built fresh at the start of each pipeline run; stages may
// share it but never mutate it.
type Snapshot struct {
	Site      config.Site
	BaseURL   string // strings.TrimRight(Site.BaseURL, "/")
	Theme     *theme.Theme
	Posts     []*post.Post  // newest-first
	Drafts    []*post.Draft // newest-Created first
	Mentions  map[string][]webmention.Mention
	Media     []MediaFile
	DraftSalt []byte

	// AssetHashes maps a path under the active theme's assets/ subtree
	// to its 8-char content hash. Built once per snapshot so every
	// stage that resolves asset_url (post pages, index, drafts) and
	// ThemeAssetStage itself share the same precomputed table.
	AssetHashes map[string]string

	// Templates is the parsed Liquid template set for the active
	// theme. Compiled once per snapshot so the three HTML stages
	// (PostPage, Index, Draft) don't each re-parse the same three
	// templates.
	Templates *templateSet

	// ThemeData is the {name, version, settings} view templates see
	// as `theme`. Built once so the HTML stages share one map.
	ThemeData map[string]any
}

// SnapshotSources holds the live sources a Pipeline reads to build a
// Snapshot. Populated once at construction and reused for every build.
//
// The pieces that come from disk (config, active theme) are reloaded
// at the start of every Build so an edit to config.yml or
// themes/<name>/* takes effect on the next render cycle without a
// process restart. The post store, webmention service, media dir, and
// draft salt are stable for the process lifetime.
type SnapshotSources struct {
	BootCfg    *config.Config // boot-time defaults; used to fill fields the live config doesn't reload (rate limits, paths)
	ConfigPath string         // path passed to config.Load on every build
	ThemesFS   fs.FS          // embedded themes/ tree, used as the fallback floor by theme.Load
	Posts      *post.Store
	WM         *webmention.Service
	MediaDir   string // path to media/, the parent of media/orig
	DraftSalt  []byte
}

// Build assembles the Snapshot. Disk-resident sources (config, theme,
// posts) are reloaded so out-of-band edits become visible on the next
// build without a restart.
func (s *SnapshotSources) Build(ctx context.Context) (*Snapshot, error) {
	// Live-reload config so edits to config.yml (site title, base URL,
	// theme settings) propagate. Falls back to the boot config if the
	// file is missing or unparseable — a config that's mid-edit
	// shouldn't crash the renderer — but log loudly so a persistent
	// problem is visible. A nil ConfigPath (test setups, never seen
	// in production) skips the reload entirely.
	cfg := s.BootCfg
	if s.ConfigPath != "" {
		if reloaded, err := config.Load(s.ConfigPath); err != nil {
			log.Printf("render: config reload failed (%v) — using boot config", err)
		} else {
			cfg = reloaded
		}
	}

	// Live-reload theme so disk theme edits propagate. theme.Load
	// reopens the on-disk root (via os.OpenRoot) on every call, so a
	// fresh Load picks up file changes.
	activeTheme, err := theme.Load(cfg.Theme.Name, s.ThemesFS, cfg.Theme.Settings)
	if err != nil {
		return nil, fmt.Errorf("reload theme: %w", err)
	}

	if err := s.Posts.Reload(); err != nil {
		return nil, fmt.Errorf("reload posts: %w", err)
	}

	media, err := listMedia(filepath.Join(s.MediaDir, "orig"))
	if err != nil {
		return nil, fmt.Errorf("list media: %w", err)
	}

	mentions := map[string][]webmention.Mention{}
	if s.WM != nil {
		all, err := s.WM.AllVerified(ctx)
		if err != nil {
			return nil, fmt.Errorf("load mentions: %w", err)
		}
		for _, m := range all {
			// Normalize the target before keying: a sender that POSTs
			// "https://site/post/" with a trailing slash would otherwise
			// never match the lookup key the render side builds from
			// snap.BaseURL + p.Path() (which never has a trailing slash).
			// Silently dropping verified mentions from the rendered page
			// is the worst possible failure mode for this feature.
			key := strings.TrimRight(m.Target, "/")
			mentions[key] = append(mentions[key], m)
		}
	}

	baseURL := strings.TrimRight(cfg.Site.BaseURL, "/")

	assetHashes, err := hashThemeAssets(activeTheme.FS)
	if err != nil {
		return nil, fmt.Errorf("hash theme assets: %w", err)
	}

	snap := &Snapshot{
		Site:        cfg.Site,
		BaseURL:     baseURL,
		Theme:       activeTheme,
		Posts:       s.Posts.Recent(0),
		Drafts:      s.Posts.ListDrafts(),
		Mentions:    mentions,
		Media:       media,
		DraftSalt:   s.DraftSalt,
		AssetHashes: assetHashes,
		ThemeData: map[string]any{
			"name":     activeTheme.Name,
			"version":  activeTheme.Version,
			"settings": activeTheme.Settings,
		},
	}
	tpl, err := loadTemplates(activeTheme.FS, themeAssetURL(snap))
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	snap.Templates = tpl
	return snap, nil
}

// hashThemeAssets walks the active theme's assets/ subtree and returns
// a path → 8-char content hash map. Called once per snapshot so every
// stage that resolves asset_url sees the same table.
func hashThemeAssets(themeFS fs.FS) (map[string]string, error) {
	sub, err := fs.Sub(themeFS, "assets")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		out[p] = hex.EncodeToString(sum[:])[:8]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// listMedia returns every regular file under origDir as a MediaFile.
// A missing dir yields an empty list (not an error) so a fresh install
// without uploads still renders.
func listMedia(origDir string) ([]MediaFile, error) {
	entries, err := os.ReadDir(origDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]MediaFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Defense in depth: a hand-placed file named "../foo" in
		// media/orig/ would otherwise let the ImageVariantStage emit
		// "media/../foo", which filepath.Join collapses out of the
		// public tree (e.g. into state/draft_salt). All admin-uploaded
		// names are safe by construction; this gates anything an
		// operator drops by hand or via SFTP.
		if strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, MediaFile{
			Name: name,
			Path: filepath.Join(origDir, name),
			Size: info.Size(),
		})
	}
	return out, nil
}
