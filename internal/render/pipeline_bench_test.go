package render

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nchapman/mizu/internal/config"
	"github.com/nchapman/mizu/internal/post"
	"github.com/nchapman/mizu/internal/theme"
)

// markdownCorpus is a representative-ish post body. Headings + bold +
// italics + code + a list exercises every default goldmark rule, so
// the benchmark approximates real-world rendering cost.
const markdownCorpus = `# Heading

A paragraph with **bold**, *italics*, and ` + "`inline code`" + `. Lorem ipsum
dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor
incididunt ut labore et dolore magna aliqua.

## Second heading

- list item 1
- list item 2
- list item 3

` + "```go\nfunc hi() { fmt.Println(\"hi\") }\n```" + `

> a blockquote about something

Another paragraph with [a link](https://example.com) and more prose to
keep the renderer working a bit harder.
`

// benchPipeline builds a fresh pipeline backed by an in-memory theme
// and a temp content tree seeded with `n` distinct posts. Returns the
// pipeline + the path each subsequent edit can target. Accepts
// testing.TB so the same setup is reusable from a sanity-check unit
// test.
func benchPipeline(b testing.TB, n int) (*Pipeline, *post.Store, string) {
	b.Helper()
	tmp := b.TempDir()
	contentDir := filepath.Join(tmp, "content")
	if err := os.MkdirAll(filepath.Join(contentDir, "posts"), 0o755); err != nil {
		b.Fatal(err)
	}
	posts, err := post.NewStore(contentDir)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < n; i++ {
		// Distinct titles avoid the same-day slug collision rule in
		// post.Store.Create. Three letters + sequence number keep the
		// slug under 32 chars (well within filesystem limits).
		title := fmt.Sprintf("Post-%c%c%c-%d", 'A'+(i/676)%26, 'A'+(i/26)%26, 'A'+i%26, i)
		if _, err := posts.Create(title, markdownCorpus, nil); err != nil {
			b.Fatalf("seed post %d: %v", i, err)
		}
	}
	b.Chdir(b.TempDir())
	if _, err := theme.Load("default", fakeThemeFS(), nil); err != nil {
		b.Fatal(err)
	}
	cfg := &config.Config{Site: config.Site{Title: "Bench", BaseURL: "https://example.test"}}
	cfg.ApplyDefaults()
	publicDir := filepath.Join(tmp, "public")
	p, err := NewPipeline(Options{
		Sources: &SnapshotSources{
			BootCfg:   cfg,
			ThemesFS:  fakeThemeFS(),
			Posts:     posts,
			MediaDir:  filepath.Join(tmp, "media"),
			DraftSalt: []byte("bench-salt-bench-salt-bench-salt"),
		},
		PublicDir: publicDir,
		HashPath:  filepath.Join(tmp, "build.json"),
	})
	if err != nil {
		b.Fatal(err)
	}
	return p, posts, contentDir
}

// BenchmarkPipeline_BuildCold measures a full first build — no
// existing hash state, every output written to disk for the first
// time. This is the worst-case wall time the operator sees: the
// startup-reconcile cost on a fresh deployment.
func BenchmarkPipeline_BuildCold(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("posts=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				p, _, _ := benchPipeline(b, n)
				b.StartTimer()
				if err := p.Build(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkPipeline_BuildWarmNoChanges measures the steady-state cost
// of a no-op build — every signal where nothing actually changed. The
// hash-skip path is the only thing keeping this fast; this benchmark
// is the canary for any regression that defeats it (a stage that
// emits non-deterministic bytes, an unintentional time.Now() in a
// stage, etc.).
func BenchmarkPipeline_BuildWarmNoChanges(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("posts=%d", n), func(b *testing.B) {
			p, _, _ := benchPipeline(b, n)
			// Prime the hash state.
			if err := p.Build(context.Background()); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := p.Build(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkPipeline_BuildOnePostEdit measures the realistic
// incremental case — operator saved one post, watcher fires, pipeline
// re-renders everything but only writes the affected outputs (the
// post's permalink + the index pages it appears on + feed.xml +
// sitemap.xml). The render cost is full-corpus; the I/O cost is
// minimal. Comparing this to BuildCold shows what fraction of cold
// time is render vs disk I/O.
func BenchmarkPipeline_BuildOnePostEdit(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("posts=%d", n), func(b *testing.B) {
			p, posts, _ := benchPipeline(b, n)
			if err := p.Build(context.Background()); err != nil {
				b.Fatal(err)
			}
			// Edit one post per iteration via Store.Update so the
			// loop measures the realistic save→rebuild path. Pick a
			// post deterministically to avoid skew across iterations.
			all := posts.Recent(0)
			if len(all) == 0 {
				b.Fatal("no posts seeded")
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				target := all[i%len(all)]
				newBody := markdownCorpus + fmt.Sprintf("\n\nedit %d\n", i)
				if _, err := posts.Update(target.ID, target.Title, newBody, target.Tags); err != nil {
					b.Fatalf("update: %v", err)
				}
				if err := p.Build(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkPipeline_ImageVariantSkip measures the input-hash
// short-circuit on the most expensive stage. With 20 images cached
// in build.json, a build that doesn't touch any image must skip
// every decode+resize. Numbers from this benchmark are what justify
// the InputHash machinery's complexity.
func BenchmarkPipeline_ImageVariantSkip(b *testing.B) {
	tmp := b.TempDir()
	contentDir := filepath.Join(tmp, "content")
	if err := os.MkdirAll(filepath.Join(contentDir, "posts"), 0o755); err != nil {
		b.Fatal(err)
	}
	mediaDir := filepath.Join(tmp, "media", "orig")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		b.Fatal(err)
	}
	// 20 distinct PNGs at modest size — large enough that decode is
	// non-trivial, small enough that the bench setup completes
	// quickly. Each one a different solid color so the encoded bytes
	// differ.
	for i := 0; i < 20; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 800, 600))
		c := color.RGBA{uint8(i * 12), uint8(255 - i*12), uint8(i * 6), 255}
		for y := 0; y < 600; y++ {
			for x := 0; x < 800; x++ {
				img.Set(x, y, c)
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mediaDir, fmt.Sprintf("img-%02d.png", i)), buf.Bytes(), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	posts, err := post.NewStore(contentDir)
	if err != nil {
		b.Fatal(err)
	}
	b.Chdir(b.TempDir())
	if _, err := theme.Load("default", fakeThemeFS(), nil); err != nil {
		b.Fatal(err)
	}
	cfg := &config.Config{Site: config.Site{Title: "Bench", BaseURL: "https://example.test"}}
	cfg.ApplyDefaults()
	publicDir := filepath.Join(tmp, "public")
	p, err := NewPipeline(Options{
		Sources: &SnapshotSources{
			BootCfg:   cfg,
			ThemesFS:  fakeThemeFS(),
			Posts:     posts,
			MediaDir:  filepath.Join(tmp, "media"),
			DraftSalt: []byte("bench-salt-bench-salt-bench-salt"),
		},
		PublicDir: publicDir,
		HashPath:  filepath.Join(tmp, "build.json"),
	})
	if err != nil {
		b.Fatal(err)
	}

	b.Run("cold", func(b *testing.B) {
		// Each iteration starts with no input-hash state so every
		// image goes through decode+resize+encode.
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			p.hashes.Inputs = map[string]string{}
			b.StartTimer()
			if err := p.Build(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("warm", func(b *testing.B) {
		// Prime once; subsequent builds should short-circuit every
		// image via the input-hash check.
		if err := p.Build(context.Background()); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := p.Build(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// Sanity check: confirm the bench harness builds something non-trivial.
// Run with `go test ./internal/render/ -run TestBenchSetup` if you suspect
// the corpus generation is broken before launching a long bench.
func TestBenchSetup(t *testing.T) {
	p, _, _ := benchPipeline(t, 5)
	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	idx := filepath.Join(p.publicDir, "index.html")
	body, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Post-AAA") {
		t.Errorf("seeded posts not present in baked index: %s", body)
	}
}
