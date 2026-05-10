package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/nchapman/mizu/internal/config"
	"github.com/nchapman/mizu/internal/post"
	"github.com/nchapman/mizu/internal/theme"
)

// fakeThemeFS mirrors what the embedded themes/ tree looks like at
// runtime: a single "default" theme with templates and one CSS asset.
func fakeThemeFS() fstest.MapFS {
	return fstest.MapFS{
		"default/theme.yaml": &fstest.MapFile{Data: []byte(`name: Test
version: "1"
settings:
  accent_color: "#0066cc"
`)},
		"default/base.liquid":      &fstest.MapFile{Data: []byte(`<!doctype html><html><head><title>{{ page_title | default: site.Title }}</title><link rel="stylesheet" href="{{ "style.css" | asset_url }}"></head><body>{{ content_for_layout }}</body></html>`)},
		"default/index.liquid":     &fstest.MapFile{Data: []byte(`{% for p in posts %}<article><a href="{{ p.Path }}">{{ p.Title | default: "note" }}</a>{{ p.HTML }}</article>{% else %}<p>none</p>{% endfor %}`)},
		"default/post.liquid":      &fstest.MapFile{Data: []byte(`<article>{% if post.Title %}<h2>{{ post.Title }}</h2>{% endif %}{{ post.HTML }}</article>{% if mentions %}<ul>{% for m in mentions %}<li>{{ m.Source }}</li>{% endfor %}</ul>{% endif %}`)},
		"default/assets/style.css": &fstest.MapFile{Data: []byte(`body{color:#222}`)},
	}
}

// newTestPipeline builds a Pipeline with a temp content/state/public
// tree and a default theme loaded from an in-memory FS. The chdir to a
// clean dir ensures theme.Load's disk fallback can't pick up the repo's
// real themes/.
func newTestPipeline(t *testing.T) (*Pipeline, *post.Store, string) {
	t.Helper()
	tmp := t.TempDir()
	contentDir := filepath.Join(tmp, "content")
	if err := os.MkdirAll(filepath.Join(contentDir, "posts"), 0o755); err != nil {
		t.Fatal(err)
	}
	posts, err := post.NewStore(contentDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	th, err := theme.Load("default", fakeThemeFS(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Site: config.Site{Title: "Test", BaseURL: "https://example.test"}}
	cfg.ApplyDefaults()

	publicDir := filepath.Join(tmp, "public")
	hashPath := filepath.Join(tmp, "build.json")
	salt := []byte("test-salt-test-salt-test-salt-aa")

	p, err := NewPipeline(Options{
		Sources: &SnapshotSources{
			Cfg:       cfg,
			Posts:     posts,
			Theme:     th,
			MediaDir:  filepath.Join(tmp, "media"),
			DraftSalt: salt,
		},
		PublicDir: publicDir,
		HashPath:  hashPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p, posts, publicDir
}

func TestPipeline_BuildEmptyStore(t *testing.T) {
	// An empty content store should still produce index.html, feed.xml,
	// sitemap.xml, robots.txt, and the theme's style.css — the floor
	// any deployment needs.
	p, _, publicDir := newTestPipeline(t)
	if err := p.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{"index.html", "feed.xml", "sitemap.xml", "robots.txt", "assets/style.css"} {
		if _, err := os.Stat(filepath.Join(publicDir, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
}

func TestPipeline_BuildRendersPostsAndIndex(t *testing.T) {
	p, posts, publicDir := newTestPipeline(t)
	a, _ := posts.Create("Hello World", "body **bold**", nil)
	n, _ := posts.Create("", "just a note", nil)

	if err := p.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	articlePath := filepath.Join(publicDir, strings.TrimPrefix(a.Path(), "/"), "index.html")
	notePath := filepath.Join(publicDir, "notes", n.ID, "index.html")
	for _, p := range []string{articlePath, notePath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	idx, err := os.ReadFile(filepath.Join(publicDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(idx), "Hello World") {
		t.Errorf("index missing post title: %s", idx)
	}
	// Article body must contain the markdown-rendered HTML, not
	// HTML-escaped markdown.
	body, _ := os.ReadFile(articlePath)
	if !strings.Contains(string(body), "<strong>bold</strong>") {
		t.Errorf("article body missing rendered markdown: %s", body)
	}
}

func TestPipeline_SkipsUnchangedOutputs(t *testing.T) {
	// A second Build with no source changes should not rewrite files
	// (content hashes match). Measure via mtime: if it changes, we
	// rewrote.
	p, posts, publicDir := newTestPipeline(t)
	posts.Create("First", "body", nil)
	if err := p.Build(context.Background()); err != nil {
		t.Fatalf("first build: %v", err)
	}
	idx := filepath.Join(publicDir, "index.html")
	st1, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}

	if err := p.Build(context.Background()); err != nil {
		t.Fatalf("second build: %v", err)
	}
	st2, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Errorf("index.html rewritten despite no source change: %v -> %v", st1.ModTime(), st2.ModTime())
	}
}

func TestPipeline_GCRemovesOrphans(t *testing.T) {
	// Deleting a post should remove its baked HTML on the next build.
	p, posts, publicDir := newTestPipeline(t)
	a, _ := posts.Create("Goodbye", "body", nil)
	if err := p.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	articleDir := filepath.Join(publicDir, strings.TrimPrefix(a.Path(), "/"))
	if _, err := os.Stat(filepath.Join(articleDir, "index.html")); err != nil {
		t.Fatalf("article not produced: %v", err)
	}
	if err := posts.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.Build(context.Background()); err != nil {
		t.Fatalf("Build after delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(articleDir, "index.html")); !os.IsNotExist(err) {
		t.Errorf("orphan article still present after delete: err=%v", err)
	}
}

func TestPipeline_DraftRendersToSaltedPath(t *testing.T) {
	p, posts, publicDir := newTestPipeline(t)
	d, err := posts.CreateDraft("Secret", "body", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	salt := []byte("test-salt-test-salt-test-salt-aa")
	want := filepath.Join(publicDir, "_drafts", DraftSlug(salt, d.ID), "index.html")
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("draft not at %s: %v", want, err)
	}
	if !strings.Contains(string(body), "Secret") {
		t.Errorf("draft missing title: %s", body)
	}
}

func TestPipeline_PaginationProducesPageFiles(t *testing.T) {
	p, posts, publicDir := newTestPipeline(t)
	// 25 posts → 2 pages at 20/page. Distinct titles avoid the
	// same-day slug collision Store.Create enforces.
	for i := 0; i < 25; i++ {
		title := "Post-" + string(rune('a'+i%26))
		if _, err := posts.Create(title+strings.Repeat("x", i), "body", nil); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if err := p.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	page2 := filepath.Join(publicDir, "page", "2", "index.html")
	if _, err := os.Stat(page2); err != nil {
		t.Errorf("page 2 missing: %v", err)
	}
	// No phantom page 3.
	if _, err := os.Stat(filepath.Join(publicDir, "page", "3", "index.html")); !os.IsNotExist(err) {
		t.Errorf("phantom page 3 exists")
	}
}

func TestPipeline_ThemeAssetServedAtPlainPath(t *testing.T) {
	p, _, publicDir := newTestPipeline(t)
	if err := p.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(publicDir, "assets", "style.css"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "body{color:#222}" {
		t.Errorf("css body mismatch: %s", got)
	}
	// Index html should reference the asset with a content-hashed query.
	idx, _ := os.ReadFile(filepath.Join(publicDir, "index.html"))
	if !strings.Contains(string(idx), "style.css?v=") {
		t.Errorf("asset_url did not produce content-hashed link: %s", idx)
	}
}

func TestDraftSlug_StablePerInput(t *testing.T) {
	salt := []byte("salty")
	a := DraftSlug(salt, "draft1")
	b := DraftSlug(salt, "draft1")
	if a != b {
		t.Errorf("non-deterministic: %q vs %q", a, b)
	}
	if DraftSlug(salt, "draft2") == a {
		t.Errorf("collision across different ids")
	}
	if DraftSlug([]byte("other"), "draft1") == a {
		t.Errorf("salt change did not change slug")
	}
}

func TestPipeline_AtomicWriteLeavesNoStaging(t *testing.T) {
	// After a successful build no .tmp-* files should remain.
	p, posts, publicDir := newTestPipeline(t)
	posts.Create("First", "body", nil)
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := filepath.Walk(publicDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.Contains(filepath.Base(path), ".tmp-") {
			t.Errorf("stray temp file: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
