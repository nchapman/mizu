package feeds

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noopValidate accepts every URL unchanged. Tests use it so they can
// subscribe to httptest servers without tripping safehttp's loopback
// guard (which is the right behavior in production).
func noopValidate(_ context.Context, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrInvalidURL
	}
	return raw, nil
}

func newService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := NewService(st, filepath.Join(dir, "subs.opml"), "My Site")
	s.validate = noopValidate
	return s
}

func TestSubscribe_AddsAndPersistsToOPML(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	feed, err := s.Subscribe(ctx, "https://a/feed", "A", "https://a/", "tech")
	if err != nil {
		t.Fatal(err)
	}
	if feed.URL != "https://a/feed" || feed.Title != "A" {
		t.Errorf("got %+v", feed)
	}
	feeds, err := s.ListFeeds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 1 {
		t.Errorf("ListFeeds len=%d", len(feeds))
	}
	subs, err := LoadOPML(s.opmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].FeedURL != "https://a/feed" || subs[0].Category != "tech" {
		t.Errorf("OPML = %+v", subs)
	}
}

func TestSubscribe_DuplicateUpdatesMetadata(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if _, err := s.Subscribe(ctx, "https://a/feed", "A", "https://a/", "tech"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Subscribe(ctx, "https://a/feed", "Renamed", "", "news"); err != nil {
		t.Fatal(err)
	}
	feeds, _ := s.ListFeeds(ctx)
	if len(feeds) != 1 {
		t.Fatalf("len=%d, want 1 (duplicate URL)", len(feeds))
	}
	if feeds[0].Title != "Renamed" || feeds[0].Category != "news" {
		t.Errorf("got %+v", feeds[0])
	}
}

func TestSubscribe_RejectsInvalid(t *testing.T) {
	s := newService(t)
	if _, err := s.Subscribe(context.Background(), "  ", "", "", ""); !errors.Is(err, ErrInvalidURL) {
		t.Errorf("got %v, want ErrInvalidURL", err)
	}
}

func TestUnsubscribe_RemovesFromBothLayers(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if _, err := s.Subscribe(ctx, "https://a/feed", "A", "https://a/", "tech"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Subscribe(ctx, "https://b/feed", "B", "https://b/", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Unsubscribe(ctx, "https://a/feed"); err != nil {
		t.Fatal(err)
	}
	feeds, _ := s.ListFeeds(ctx)
	if len(feeds) != 1 || feeds[0].URL != "https://b/feed" {
		t.Errorf("after unsubscribe = %+v", feeds)
	}
	subs, _ := LoadOPML(s.opmlPath)
	if len(subs) != 1 || subs[0].FeedURL != "https://b/feed" {
		t.Errorf("OPML after unsubscribe = %+v", subs)
	}
}

func TestLoadFromOPML_ReconcilesDB(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	// Pre-seed a feed in the DB only.
	if _, err := s.Store.UpsertFeed(ctx, &Feed{URL: "https://stale/feed", Title: "Stale"}); err != nil {
		t.Fatal(err)
	}
	// Write OPML with a different set.
	in := []Subscription{
		{Title: "Kept", FeedURL: "https://kept/feed", SiteURL: "https://kept/", Category: "tech"},
	}
	if err := SaveOPML(s.opmlPath, s.siteTitle, in); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadFromOPML(ctx); err != nil {
		t.Fatal(err)
	}
	feeds, _ := s.ListFeeds(ctx)
	if len(feeds) != 1 || feeds[0].URL != "https://kept/feed" {
		t.Errorf("after LoadFromOPML = %+v (stale feed should be removed)", feeds)
	}
}

func TestLoadFromOPML_MissingFileIsNoError(t *testing.T) {
	s := newService(t)
	// opmlPath does not exist on disk yet.
	if _, err := os.Stat(s.opmlPath); err == nil {
		t.Skip("opml unexpectedly present")
	}
	if err := s.LoadFromOPML(context.Background()); err != nil {
		t.Errorf("LoadFromOPML on missing OPML = %v, want nil", err)
	}
}

func TestValidateFeedURL(t *testing.T) {
	ctx := context.Background()
	if _, err := validateFeedURL(ctx, "ftp://x/y"); !errors.Is(err, ErrInvalidURL) {
		t.Errorf("ftp scheme err=%v, want ErrInvalidURL", err)
	}
	if _, err := validateFeedURL(ctx, "https://"); !errors.Is(err, ErrInvalidURL) {
		t.Errorf("empty host err=%v", err)
	}
	if _, err := validateFeedURL(ctx, "http://127.0.0.1/feed"); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("loopback IP literal err=%v, want ErrBlockedAddress", err)
	}
	if _, err := validateFeedURL(ctx, "http://10.0.0.1/feed"); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("rfc1918 IP literal err=%v, want ErrBlockedAddress", err)
	}
	// Public IP literal should pass validation. (We don't actually
	// fetch it; validate just confirms the address class.)
	if got, err := validateFeedURL(ctx, "https://8.8.8.8/feed"); err != nil || got == "" {
		t.Errorf("public IP got=%q err=%v", got, err)
	}
}
