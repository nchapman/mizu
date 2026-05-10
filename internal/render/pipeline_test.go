package render

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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
	// Verify the theme parses cleanly at construction. The pipeline
	// itself re-loads on every build via SnapshotSources.ThemesFS.
	if _, err := theme.Load("default", fakeThemeFS(), nil); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Site: config.Site{Title: "Test", BaseURL: "https://example.test"}}
	cfg.ApplyDefaults()

	publicDir := filepath.Join(tmp, "public")
	hashPath := filepath.Join(tmp, "build.json")
	salt := []byte("test-salt-test-salt-test-salt-aa")

	p, err := NewPipeline(Options{
		Sources: &SnapshotSources{
			BootCfg:   cfg,
			ThemesFS:  fakeThemeFS(),
			Posts:     posts,
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

func TestPipeline_ConfigReloadAffectsNextBuild(t *testing.T) {
	// Editing config.yml's site.title must show up in the next build
	// without a process restart. Regression for an earlier shape where
	// cfg was loaded once and the pipeline never re-read it.
	p, posts, publicDir := newTestPipeline(t)
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yml")
	writeCfg := func(title string) {
		body := "site:\n  title: " + title + "\n  base_url: https://example.test\npaths:\n  content: " + filepath.Join(tmp, "content") + "\n  media: " + filepath.Join(tmp, "media") + "\n  cache: " + filepath.Join(tmp, "cache") + "\n  state: " + filepath.Join(tmp, "state") + "\n"
		if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeCfg("First")
	p.sources.ConfigPath = cfgPath
	posts.Create("Hello", "body", nil)
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	idx, _ := os.ReadFile(filepath.Join(publicDir, "index.html"))
	if !strings.Contains(string(idx), "<title>First</title>") {
		t.Fatalf("first build missing initial title: %s", idx)
	}

	writeCfg("Second")
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	idx, _ = os.ReadFile(filepath.Join(publicDir, "index.html"))
	if !strings.Contains(string(idx), "<title>Second</title>") {
		t.Errorf("config edit not reflected after rebuild: %s", idx)
	}
}

func TestPipeline_ThemeFSReloadsEveryBuild(t *testing.T) {
	// Edits to the theme FS must propagate without a process restart.
	// Use a mutable fstest.MapFS as the themes tree; mutate a template
	// between builds and assert the second build picks up the change.
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
	themeFS := fakeThemeFS()
	if _, err := theme.Load("default", themeFS, nil); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Site: config.Site{Title: "Test", BaseURL: "https://example.test"}}
	cfg.ApplyDefaults()
	publicDir := filepath.Join(tmp, "public")
	p, err := NewPipeline(Options{
		Sources: &SnapshotSources{
			BootCfg:   cfg,
			ThemesFS:  themeFS,
			Posts:     posts,
			MediaDir:  filepath.Join(tmp, "media"),
			DraftSalt: []byte("salt-salt-salt-salt-salt-salt-aa"),
		},
		PublicDir: publicDir,
		HashPath:  filepath.Join(tmp, "build.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	idx, _ := os.ReadFile(filepath.Join(publicDir, "index.html"))
	if strings.Contains(string(idx), "themed-marker-banner") {
		t.Fatalf("baseline build already contains the post-edit marker: %s", idx)
	}

	// Mutate base.liquid in place. theme.Load on the next Build re-
	// reads the MapFS, so the change should land.
	themeFS["default/base.liquid"].Data = []byte(`<!doctype html><html><body><div class="themed-marker-banner">{{ content_for_layout }}</div></body></html>`)
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	idx, _ = os.ReadFile(filepath.Join(publicDir, "index.html"))
	if !strings.Contains(string(idx), "themed-marker-banner") {
		t.Errorf("theme edit not reflected after rebuild: %s", idx)
	}
}

func TestPipeline_PostHTMLCachedAcrossBuilds(t *testing.T) {
	// Editing one post in a corpus of N must run goldmark exactly
	// once on the next build — for the post that changed. The
	// untouched posts come from the markdown cache.
	//
	// We can't easily count goldmark calls without instrumentation,
	// so the contract is asserted indirectly: after the first build,
	// p.mdCache holds an entry for each post whose body hash matches
	// what was rendered. Editing a post invalidates exactly one
	// entry; the rest survive untouched (same pointer is reused via
	// the cache hit path).
	p, posts, _ := newTestPipeline(t)
	a, _ := posts.Create("First", "body one", nil)
	b, _ := posts.Create("Second", "body two", nil)
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.mdCache) != 2 {
		t.Fatalf("mdCache size after first build = %d, want 2", len(p.mdCache))
	}
	beforeA := p.mdCache[a.ID]
	beforeB := p.mdCache[b.ID]

	// Edit only post B. Cache entry for A must be byte-identical
	// (same pointer to the same string is fine; we compare values).
	if _, err := posts.Update(b.ID, b.Title, "body two — edited", b.Tags); err != nil {
		t.Fatal(err)
	}
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.mdCache[a.ID]; got != beforeA {
		t.Errorf("post A's cache entry was rewritten despite no edit: %+v vs %+v", beforeA, got)
	}
	if got := p.mdCache[b.ID]; got == beforeB {
		t.Errorf("post B's cache entry survived an edit: %+v", got)
	}
	if !strings.Contains(p.mdCache[b.ID].HTML, "edited") {
		t.Errorf("post B's cache entry missing post-edit content: %+v", p.mdCache[b.ID])
	}

	// Deleting a post must purge its cache entry — otherwise a long-
	// running process accumulates cruft for every published-then-
	// deleted post.
	if err := posts.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, still := p.mdCache[a.ID]; still {
		t.Errorf("deleted post's cache entry leaked: %+v", p.mdCache[a.ID])
	}
}

func TestPipeline_TemplateCacheReuseAcrossBuilds(t *testing.T) {
	// Two builds with the same theme bytes must reuse the same
	// parsed templateSet — the cache hit is observable via pointer
	// identity on p.tplCache.set.
	p, _, _ := newTestPipeline(t)
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := p.tplCache
	if first == nil {
		t.Fatal("tplCache not populated after first build")
	}
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.tplCache != first {
		t.Errorf("tplCache replaced despite no template change: prev=%p now=%p", first, p.tplCache)
	}
}

func TestPipeline_ImageVariantSkipsDecodeWhenSourceUnchanged(t *testing.T) {
	// After the first build records an input fingerprint for an
	// image, a second build must NOT rewrite the output (mtime
	// preserved) and must keep the same on-disk variant. Touching
	// the source file with a fresh mtime forces a re-render.
	tmp := t.TempDir()
	contentDir := filepath.Join(tmp, "content")
	if err := os.MkdirAll(filepath.Join(contentDir, "posts"), 0o755); err != nil {
		t.Fatal(err)
	}
	mediaDir := filepath.Join(tmp, "media", "orig")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Real PNG bytes via image/png.Encode — a hand-rolled header here
	// would silently diverge from the production sniff path, and an
	// invalid IDAT chunk would fail decode in the stage.
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	imgPath := filepath.Join(mediaDir, "img.png")
	if err := os.WriteFile(imgPath, pngBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	posts, err := post.NewStore(contentDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if _, err := theme.Load("default", fakeThemeFS(), nil); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Site: config.Site{Title: "Test", BaseURL: "https://example.test"}}
	cfg.ApplyDefaults()
	publicDir := filepath.Join(tmp, "public")
	p, err := NewPipeline(Options{
		Sources: &SnapshotSources{
			BootCfg:   cfg,
			ThemesFS:  fakeThemeFS(),
			Posts:     posts,
			MediaDir:  filepath.Join(tmp, "media"),
			DraftSalt: []byte("salt-salt-salt-salt-salt-salt-aa"),
		},
		PublicDir: publicDir,
		HashPath:  filepath.Join(tmp, "build.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	variant := filepath.Join(publicDir, "media", "img.png")
	st1, err := os.Stat(variant)
	if err != nil {
		t.Fatalf("variant not produced: %v", err)
	}

	// Second build with no source change — variant must not be rewritten.
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	st2, err := os.Stat(variant)
	if err != nil {
		t.Fatal(err)
	}
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Errorf("variant was rewritten with no source change: %v -> %v", st1.ModTime(), st2.ModTime())
	}

	// Replace the image with different content (blue) and bump the
	// mtime. The new render must produce different bytes than the
	// red original, forcing the write.
	img2 := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img2.Set(x, y, color.RGBA{0, 0, 255, 255})
		}
	}
	var pngBuf2 bytes.Buffer
	if err := png.Encode(&pngBuf2, img2); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imgPath, pngBuf2.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	future := st1.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(imgPath, future, future); err != nil {
		t.Fatal(err)
	}
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	st3, err := os.Stat(variant)
	if err != nil {
		t.Fatal(err)
	}
	if st3.ModTime().Equal(st2.ModTime()) {
		t.Errorf("variant was NOT rewritten after source content change")
	}
	// And the on-disk variant must now reflect the new (blue) source.
	got, err := os.ReadFile(variant)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decoded.At(0, 0).RGBA()
	if r > 0x4000 || g > 0x4000 || b < 0xa000 {
		t.Errorf("variant pixel = (%x, %x, %x), expected mostly blue", r>>8, g>>8, b>>8)
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
