package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nchapman/mizu/internal/feeds"
)

// seedPost writes a markdown post file with frontmatter at the given date,
// then triggers a Reload so the store picks it up. Going through the file
// system rather than Store.Create lets us pin Date to a known value for
// merge-ordering assertions.
func seedPost(t *testing.T, h *harness, id, title, body string, date time.Time) {
	t.Helper()
	postsDir, _ := h.posts.Dirs()
	raw := fmt.Sprintf("---\nid: %s\ntitle: %s\ndate: %s\n---\n\n%s\n",
		id, title, date.UTC().Format(time.RFC3339), body)
	fname := fmt.Sprintf("%s-%s.md", date.UTC().Format("2006-01-02"), id)
	if err := os.WriteFile(filepath.Join(postsDir, fname), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.posts.Reload(); err != nil {
		t.Fatal(err)
	}
}

func seedFeedItem(t *testing.T, h *harness, feedID int64, guid, title string, published time.Time) {
	t.Helper()
	if _, err := h.feeds.Store.InsertItem(context.Background(), &feeds.Item{
		FeedID:      feedID,
		GUID:        guid,
		Title:       title,
		PublishedAt: published,
		FetchedAt:   time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStream_MergesPostsAndFeedItemsByDate(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)

	feedID, err := h.feeds.Store.UpsertFeed(context.Background(), &feeds.Feed{URL: "https://a/", Title: "FeedA"})
	if err != nil {
		t.Fatal(err)
	}

	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)
	t4 := time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)

	seedFeedItem(t, h, feedID, "g-t1", "Feed-T1", t1)
	seedPost(t, h, "p-t2", "Post T2", "body two", t2)
	seedFeedItem(t, h, feedID, "g-t3", "Feed-T3", t3)
	seedPost(t, h, "p-t4", "Post T4", "body four", t4)

	w := h.do(t, "GET", "/admin/api/stream?limit=10", nil, c)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var resp streamResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 4 {
		t.Fatalf("len=%d want 4: %+v", len(resp.Items), resp.Items)
	}
	// Newest first, alternating per our timestamps.
	want := []struct{ kind, label string }{
		{"own", "Post T4"},
		{"feed", "Feed-T3"},
		{"own", "Post T2"},
		{"feed", "Feed-T1"},
	}
	for i, it := range resp.Items {
		if it.Kind != want[i].kind {
			t.Errorf("item[%d] kind=%s want %s", i, it.Kind, want[i].kind)
		}
		var got string
		if it.Item != nil {
			got = it.Item.Title
		}
		if it.Post != nil {
			got = it.Post.Title
		}
		if got != want[i].label {
			t.Errorf("item[%d] title=%q want %q", i, got, want[i].label)
		}
	}
	if resp.NextCursor != "" {
		t.Errorf("nextCursor=%q on a non-full page", resp.NextCursor)
	}
}

func TestStream_PaginatesAcrossSourcesViaCursor(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)

	feedID, _ := h.feeds.Store.UpsertFeed(context.Background(), &feeds.Feed{URL: "https://a/", Title: "FeedA"})
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)
	t4 := time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)
	seedFeedItem(t, h, feedID, "g-t1", "Feed-T1", t1)
	seedPost(t, h, "p-t2", "Post T2", "two", t2)
	seedFeedItem(t, h, feedID, "g-t3", "Feed-T3", t3)
	seedPost(t, h, "p-t4", "Post T4", "four", t4)

	w := h.do(t, "GET", "/admin/api/stream?limit=2", nil, c)
	var p1 streamResponse
	if err := json.NewDecoder(w.Body).Decode(&p1); err != nil {
		t.Fatal(err)
	}
	if len(p1.Items) != 2 {
		t.Fatalf("page1 len=%d", len(p1.Items))
	}
	if p1.NextCursor == "" {
		t.Fatalf("page1 next_cursor empty on full page")
	}
	if p1.Items[0].Post == nil || p1.Items[0].Post.Title != "Post T4" {
		t.Errorf("page1[0] = %+v", p1.Items[0])
	}
	if p1.Items[1].Item == nil || p1.Items[1].Item.Title != "Feed-T3" {
		t.Errorf("page1[1] = %+v", p1.Items[1])
	}

	w = h.do(t, "GET", "/admin/api/stream?limit=2&cursor="+url.QueryEscape(p1.NextCursor), nil, c)
	var p2 streamResponse
	if err := json.NewDecoder(w.Body).Decode(&p2); err != nil {
		t.Fatal(err)
	}
	if len(p2.Items) != 2 {
		t.Fatalf("page2 len=%d", len(p2.Items))
	}
	if p2.Items[0].Post == nil || p2.Items[0].Post.Title != "Post T2" {
		t.Errorf("page2[0] = %+v", p2.Items[0])
	}
	if p2.Items[1].Item == nil || p2.Items[1].Item.Title != "Feed-T1" {
		t.Errorf("page2[1] = %+v", p2.Items[1])
	}
}

func TestStream_FilterYoursReturnsPostsOnly(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)

	feedID, _ := h.feeds.Store.UpsertFeed(context.Background(), &feeds.Feed{URL: "https://a/", Title: "FeedA"})
	seedFeedItem(t, h, feedID, "g1", "F", time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC))
	seedPost(t, h, "p1", "Mine", "x", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))

	w := h.do(t, "GET", "/admin/api/stream?filter=yours", nil, c)
	var resp streamResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Items) != 1 || resp.Items[0].Kind != "own" || resp.Items[0].Post.Title != "Mine" {
		t.Errorf("yours filter = %+v", resp.Items)
	}
}

func TestStream_FilterFollowingReturnsFeedsOnly(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)

	feedID, _ := h.feeds.Store.UpsertFeed(context.Background(), &feeds.Feed{URL: "https://a/", Title: "FeedA"})
	seedFeedItem(t, h, feedID, "g1", "Feed item", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	seedPost(t, h, "p1", "Mine", "x", time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC))

	w := h.do(t, "GET", "/admin/api/stream?filter=following", nil, c)
	var resp streamResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Items) != 1 || resp.Items[0].Kind != "feed" {
		t.Errorf("following filter = %+v", resp.Items)
	}
}

func TestStream_FilterUnreadHidesReadFeedsAndAllPosts(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	ctx := context.Background()

	feedID, _ := h.feeds.Store.UpsertFeed(ctx, &feeds.Feed{URL: "https://a/", Title: "A"})
	seedFeedItem(t, h, feedID, "g-old", "Old", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	seedFeedItem(t, h, feedID, "g-new", "New", time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC))
	seedPost(t, h, "p1", "Mine", "x", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))

	// Mark the older feed item read.
	items, _ := h.feeds.Store.Timeline(ctx, feeds.TimelineCursor{}, 10, false)
	for _, it := range items {
		if it.Title == "Old" {
			if err := h.feeds.Store.MarkRead(ctx, it.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}

	w := h.do(t, "GET", "/admin/api/stream?filter=unread", nil, c)
	var resp streamResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Items) != 1 || resp.Items[0].Kind != "feed" || resp.Items[0].Item.Title != "New" {
		t.Errorf("unread filter = %+v", resp.Items)
	}
}

func TestStream_BadCursor(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	w := h.do(t, "GET", "/admin/api/stream?cursor=!!!notbase64!!!", nil, c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code=%d", w.Code)
	}
}

func TestStreamCursor_RoundTrip(t *testing.T) {
	c := streamCursor{
		Feed: feeds.TimelineCursor{
			PublishedAt: time.Unix(1700000000, 0),
			ID:          42,
		},
		PostBefore: time.Unix(1700001234, 0),
	}
	encoded := encodeStreamCursor(c)
	if encoded == "" {
		t.Fatal("encoded empty")
	}
	decoded, ok := decodeStreamCursor(encoded)
	if !ok {
		t.Fatal("decode failed")
	}
	if !decoded.Feed.PublishedAt.Equal(c.Feed.PublishedAt) || decoded.Feed.ID != c.Feed.ID {
		t.Errorf("feed mismatch: %+v vs %+v", decoded.Feed, c.Feed)
	}
	if !decoded.PostBefore.Equal(c.PostBefore) {
		t.Errorf("post mismatch: %v vs %v", decoded.PostBefore, c.PostBefore)
	}
	if encodeStreamCursor(streamCursor{}) != "" {
		t.Error("zero cursor should encode to empty string")
	}
}
