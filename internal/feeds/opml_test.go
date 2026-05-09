package feeds

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestLoadOPML_MissingReturnsEmpty(t *testing.T) {
	subs, err := LoadOPML(filepath.Join(t.TempDir(), "nope.opml"))
	if err != nil {
		t.Errorf("missing file err=%v, want nil", err)
	}
	if len(subs) != 0 {
		t.Errorf("got %d subs, want 0", len(subs))
	}
}

func TestLoadOPML_Malformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.opml")
	if err := os.WriteFile(path, []byte("<not really opml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOPML(path); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestSaveAndLoadOPML_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subs.opml")
	in := []Subscription{
		{Title: "Tech Blog", FeedURL: "https://a/feed.xml", SiteURL: "https://a/", Category: "tech"},
		{Title: "Personal", FeedURL: "https://b/atom", SiteURL: "https://b/", Category: ""},
		{Title: "Other Tech", FeedURL: "https://c/rss", SiteURL: "https://c/", Category: "tech"},
	}
	if err := SaveOPML(path, "My Site", in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadOPML(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("len in=%d out=%d", len(in), len(out))
	}
	// Sort both by FeedURL for stable comparison.
	sort.Slice(in, func(i, j int) bool { return in[i].FeedURL < in[j].FeedURL })
	sort.Slice(out, func(i, j int) bool { return out[i].FeedURL < out[j].FeedURL })
	for i := range in {
		if in[i] != out[i] {
			t.Errorf("[%d] in=%+v out=%+v", i, in[i], out[i])
		}
	}
}

func TestSaveOPML_AtomicTempCleanup(t *testing.T) {
	// After a successful save the .tmp file must not linger.
	dir := t.TempDir()
	path := filepath.Join(dir, "subs.opml")
	if err := SaveOPML(path, "T", []Subscription{{Title: "A", FeedURL: "https://a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error(".tmp file still present after save")
	}
}

func TestLoadOPML_FlattensNestedCategories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested.opml")
	body := `<?xml version="1.0"?>
<opml version="2.0">
  <head><title>x</title></head>
  <body>
    <outline text="tech" title="tech">
      <outline text="A" title="A" type="rss" xmlUrl="https://a/feed" htmlUrl="https://a/"/>
      <outline text="subcat">
        <outline text="B" title="B" type="rss" xmlUrl="https://b/feed" htmlUrl="https://b/"/>
      </outline>
    </outline>
  </body>
</opml>
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	subs, err := LoadOPML(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 2 {
		t.Fatalf("got %d subs", len(subs))
	}
	for _, s := range subs {
		if s.Category != "tech" {
			t.Errorf("sub %q category=%q, want flattened to 'tech'", s.FeedURL, s.Category)
		}
	}
}
