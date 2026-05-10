package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nchapman/mizu/internal/db"
	"github.com/nchapman/mizu/internal/webmention"
)

// TestPostPage_MentionTrailingSlashKeyMatches pins the trailing-slash
// normalization that lets a webmention POSTed against the trailing-
// slash form of a permalink still appear on the rendered page.
//
// Regression for an earlier shape where Snapshot.Mentions was keyed
// verbatim by m.Target. A sender that picked the canonical-with-slash
// form of a URL (".../slug/") would silently drop off the rendered
// post page because the lookup key built from post.Path() never has
// a trailing slash.
func TestPostPage_MentionTrailingSlashKeyMatches(t *testing.T) {
	p, posts, publicDir := newTestPipeline(t)
	a, err := posts.Create("Mentioned", "body", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Inject a verified mention whose target carries the trailing
	// slash. The post's permalink (post.Path()) has none.
	target := "https://example.test" + a.Path() + "/"
	now := time.Now().UTC()
	p.sources.WM = nil // bypass live SQLite; inject mentions directly
	p.sources.Posts = posts

	// Patch the snapshot build to seed Mentions: easiest way is to
	// run a Build with a stand-in WM service. Instead, set up a real
	// in-memory store and upsert a verified row.
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	wmStore := webmention.NewStore(conn)
	wm := webmention.New(wmStore, "https://example.test")
	if err := wmStore.Upsert(context.Background(), webmention.Mention{
		Source:     "https://other.example/x",
		Target:     target,
		Status:     webmention.StatusVerified,
		ReceivedAt: now,
		VerifiedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	p.sources.WM = wm

	if err := p.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	postPath := filepath.Join(publicDir, strings.TrimPrefix(a.Path(), "/"), "index.html")
	body, err := os.ReadFile(postPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "other.example") {
		t.Errorf("trailing-slash mention did not render onto target page: %s", body)
	}
}
