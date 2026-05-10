package feeds

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nchapman/mizu/internal/db"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return NewStore(conn)
}

func TestStore_UpsertFeedRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, err := st.UpsertFeed(ctx, &Feed{
		URL: "https://a/", Title: "A", SiteURL: "https://a", Category: "tech",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Errorf("id=%d", id)
	}
	feeds, err := st.ListFeeds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 1 || feeds[0].URL != "https://a/" || feeds[0].Title != "A" {
		t.Errorf("got %+v", feeds)
	}
}

func TestStore_UpsertPreservesNonEmptyMetadata(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id1, err := st.UpsertFeed(ctx, &Feed{URL: "https://a/", Title: "Original", SiteURL: "https://a"})
	if err != nil {
		t.Fatal(err)
	}
	// Re-upsert with empty title — must not blank out the existing title.
	id2, err := st.UpsertFeed(ctx, &Feed{URL: "https://a/", Title: "", Category: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("id changed across upsert: %d vs %d", id1, id2)
	}
	feeds, _ := st.ListFeeds(ctx)
	if feeds[0].Title != "Original" {
		t.Errorf("title clobbered: %q", feeds[0].Title)
	}
	if feeds[0].Category != "x" {
		t.Errorf("category not updated: %q", feeds[0].Category)
	}
}

func TestStore_DeleteFeedByURLCascadesItems(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, err := st.UpsertFeed(ctx, &Feed{URL: "https://a/", Title: "A"})
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := st.InsertItem(ctx, &Item{
		FeedID: id, GUID: "g1", URL: "https://a/p1", Title: "P1",
		PublishedAt: time.Now(), FetchedAt: time.Now(),
	})
	if err != nil || !inserted {
		t.Fatalf("InsertItem inserted=%v err=%v", inserted, err)
	}
	if err := st.DeleteFeedByURL(ctx, "https://a/"); err != nil {
		t.Fatal(err)
	}
	items, err := st.Timeline(ctx, TimelineCursor{}, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("items remained after feed delete: %d", len(items))
	}
}

func TestStore_InsertItemDedupes(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, _ := st.UpsertFeed(ctx, &Feed{URL: "https://a/"})
	now := time.Now()
	first, err := st.InsertItem(ctx, &Item{FeedID: id, GUID: "g1", FetchedAt: now})
	if err != nil || !first {
		t.Fatalf("first inserted=%v err=%v", first, err)
	}
	second, err := st.InsertItem(ctx, &Item{FeedID: id, GUID: "g1", FetchedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if second {
		t.Error("second InsertItem with same (feed,guid) returned true")
	}
}

func TestStore_TimelineOrderingAndCursor(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, _ := st.UpsertFeed(ctx, &Feed{URL: "https://a/", Title: "A"})
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := st.InsertItem(ctx, &Item{
			FeedID: id, GUID: "g" + string(rune('0'+i)),
			Title:       "I" + string(rune('0'+i)),
			PublishedAt: t0.Add(time.Duration(i) * time.Hour),
			FetchedAt:   time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	page1, err := st.Timeline(ctx, TimelineCursor{}, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 3 {
		t.Fatalf("page1 len=%d, want 3", len(page1))
	}
	// Newest first.
	if page1[0].Title != "I4" || page1[2].Title != "I2" {
		t.Errorf("page1 order = %+v", page1)
	}
	cur := TimelineCursor{PublishedAt: page1[2].PublishedAt, ID: page1[2].ID}
	page2, err := st.Timeline(ctx, cur, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].Title != "I1" || page2[1].Title != "I0" {
		t.Errorf("page2 = %+v", page2)
	}
}

func TestStore_TimelineUnreadOnly(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, _ := st.UpsertFeed(ctx, &Feed{URL: "https://a/"})
	now := time.Now()
	for _, g := range []string{"g1", "g2", "g3"} {
		st.InsertItem(ctx, &Item{FeedID: id, GUID: g, FetchedAt: now})
	}
	all, _ := st.Timeline(ctx, TimelineCursor{}, 10, false)
	if len(all) != 3 {
		t.Fatalf("got %d items", len(all))
	}
	if err := st.MarkRead(ctx, all[0].ID, true); err != nil {
		t.Fatal(err)
	}
	unread, _ := st.Timeline(ctx, TimelineCursor{}, 10, true)
	if len(unread) != 2 {
		t.Errorf("unread count=%d, want 2", len(unread))
	}
	// Unmark read.
	if err := st.MarkRead(ctx, all[0].ID, false); err != nil {
		t.Fatal(err)
	}
	unread, _ = st.Timeline(ctx, TimelineCursor{}, 10, true)
	if len(unread) != 3 {
		t.Errorf("after unmark unread=%d, want 3", len(unread))
	}
}

func TestStore_MarkRead_NotFound(t *testing.T) {
	st := newTestStore(t)
	if err := st.MarkRead(context.Background(), 9999, true); err != ErrItemNotFound {
		t.Errorf("got %v, want ErrItemNotFound", err)
	}
}

func TestStore_MarkFetchedAndError(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, _ := st.UpsertFeed(ctx, &Feed{URL: "https://a/"})

	// MarkFetched: sets metadata and fills empty title/site_url.
	if err := st.MarkFetched(ctx, id, "etag-1", "Wed, 21 Oct 2026 07:28:00 GMT", "Discovered Title", "https://discovered/"); err != nil {
		t.Fatal(err)
	}
	etag, lm, err := st.FeedFetchInfo(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if etag != "etag-1" || lm == "" {
		t.Errorf("fetch info = %q, %q", etag, lm)
	}
	feeds, _ := st.ListFeeds(ctx)
	if feeds[0].Title != "Discovered Title" || feeds[0].SiteURL != "https://discovered/" {
		t.Errorf("title/site_url not filled: %+v", feeds[0])
	}
	if feeds[0].LastFetchedAt.IsZero() {
		t.Error("LastFetchedAt zero after MarkFetched")
	}

	// Subsequent MarkFetched must NOT overwrite the now-populated title.
	if err := st.MarkFetched(ctx, id, "etag-2", "", "Different", "https://different/"); err != nil {
		t.Fatal(err)
	}
	feeds, _ = st.ListFeeds(ctx)
	if feeds[0].Title != "Discovered Title" {
		t.Errorf("title overwritten on second fetch: %q", feeds[0].Title)
	}

	// MarkFetchError populates last_error.
	if err := st.MarkFetchError(ctx, id, "boom"); err != nil {
		t.Fatal(err)
	}
	feeds, _ = st.ListFeeds(ctx)
	if feeds[0].LastError != "boom" {
		t.Errorf("LastError=%q", feeds[0].LastError)
	}
}

func TestStore_FeedFetchInfo_Unknown(t *testing.T) {
	st := newTestStore(t)
	etag, lm, err := st.FeedFetchInfo(context.Background(), 99999)
	if err != nil {
		t.Errorf("unknown feed err=%v, want nil", err)
	}
	if etag != "" || lm != "" {
		t.Errorf("got (%q, %q), want empty", etag, lm)
	}
}

func TestTimelineCursor_IsZero(t *testing.T) {
	if !(TimelineCursor{}).IsZero() {
		t.Error("zero TimelineCursor not IsZero()")
	}
	if (TimelineCursor{ID: 1}).IsZero() {
		t.Error("ID=1 reported as zero")
	}
}
