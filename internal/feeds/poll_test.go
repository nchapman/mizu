package feeds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nchapman/mizu/internal/db"
)

const sampleAtom = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Test Feed</title>
  <link href="https://example.com/"/>
  <updated>2026-05-08T00:00:00Z</updated>
  <id>tag:test</id>
  <entry>
    <id>e1</id>
    <title>First</title>
    <link href="https://example.com/p1"/>
    <updated>2026-05-08T00:00:00Z</updated>
    <content type="html">&lt;p&gt;hello&lt;script&gt;alert(1)&lt;/script&gt;&lt;/p&gt;</content>
  </entry>
  <entry>
    <id>e2</id>
    <title>Second</title>
    <link href="https://example.com/p2"/>
    <updated>2026-05-08T01:00:00Z</updated>
    <content type="html">&lt;p&gt;world&lt;/p&gt;</content>
  </entry>
</feed>`

func newPoller(t *testing.T) (*Poller, *Store) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	st := NewStore(conn)
	p := NewPoller(st, time.Hour, "test/1.0")
	// Bypass the SSRF-safe client so httptest (loopback) URLs work.
	p.http = http.DefaultClient
	return p, st
}

func TestPollOne_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "test/1.0" {
			t.Errorf("UA=%q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2026 07:28:00 GMT")
		w.Header().Set("Content-Type", "application/atom+xml")
		w.Write([]byte(sampleAtom))
	}))
	defer srv.Close()

	p, st := newPoller(t)
	ctx := context.Background()
	id, _ := st.UpsertFeed(ctx, &Feed{URL: srv.URL})
	if err := p.PollOne(ctx, Feed{ID: id, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	items, err := st.Timeline(ctx, TimelineCursor{}, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	// Sanitization: <script> should have been stripped by bluemonday.
	for _, it := range items {
		if strings.Contains(it.Content, "<script") {
			t.Errorf("content not sanitized: %q", it.Content)
		}
	}
	etag, lm, err := st.FeedFetchInfo(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if etag != `"abc"` || lm == "" {
		t.Errorf("fetch info etag=%q lm=%q", etag, lm)
	}
}

func TestPollOne_NotModifiedKeepsState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != `"abc"` {
			t.Errorf("If-None-Match=%q", r.Header.Get("If-None-Match"))
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	p, st := newPoller(t)
	ctx := context.Background()
	id, _ := st.UpsertFeed(ctx, &Feed{URL: srv.URL})
	// Seed an ETag so the request sends If-None-Match.
	if err := st.MarkFetched(ctx, id, `"abc"`, "", "Title", "https://x/"); err != nil {
		t.Fatal(err)
	}
	if err := p.PollOne(ctx, Feed{ID: id, URL: srv.URL, ETag: `"abc"`}); err != nil {
		t.Fatal(err)
	}
	items, _ := st.Timeline(ctx, TimelineCursor{}, 50, false)
	if len(items) != 0 {
		t.Errorf("304 should not insert items, got %d", len(items))
	}
}

func TestPollOne_HTTPErrorReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("oops"))
	}))
	defer srv.Close()
	p, st := newPoller(t)
	id, _ := st.UpsertFeed(context.Background(), &Feed{URL: srv.URL})
	err := p.PollOne(context.Background(), Feed{ID: id, URL: srv.URL})
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestPollAll_RecordsErrorOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	p, st := newPoller(t)
	ctx := context.Background()
	if _, err := st.UpsertFeed(ctx, &Feed{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	p.PollAll(ctx)
	feeds, _ := st.ListFeeds(ctx)
	if len(feeds) != 1 {
		t.Fatalf("len=%d", len(feeds))
	}
	if feeds[0].LastError == "" {
		t.Error("LastError empty after failed poll")
	}
}
