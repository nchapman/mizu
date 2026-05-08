package feeds

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// Service is the public face of the feeds package — it keeps the OPML file
// and SQLite store in sync as subscriptions change. Mutating operations
// take a write lock so concurrent admin requests can't reorder OPML writes.
type Service struct {
	Store     *Store
	opmlPath  string
	siteTitle string

	mu sync.Mutex
}

func NewService(store *Store, opmlPath, siteTitle string) *Service {
	return &Service{Store: store, opmlPath: opmlPath, siteTitle: siteTitle}
}

// LoadFromOPML reads the on-disk OPML and upserts each subscription into
// the DB. Call once at startup. Feeds present in the DB but missing from
// the OPML are removed (OPML is the source of truth).
func (s *Service) LoadFromOPML(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs, err := LoadOPML(s.opmlPath)
	if err != nil {
		return err
	}
	wanted := make(map[string]bool, len(subs))
	for _, sub := range subs {
		wanted[sub.FeedURL] = true
		if _, err := s.Store.UpsertFeed(ctx, &Feed{
			URL:      sub.FeedURL,
			Title:    sub.Title,
			SiteURL:  sub.SiteURL,
			Category: sub.Category,
		}); err != nil {
			return fmt.Errorf("upsert %s: %w", sub.FeedURL, err)
		}
	}
	existing, err := s.Store.ListFeeds(ctx)
	if err != nil {
		return err
	}
	for _, f := range existing {
		if !wanted[f.URL] {
			if err := s.Store.DeleteFeedByURL(ctx, f.URL); err != nil {
				return err
			}
		}
	}
	return nil
}

var ErrInvalidURL = errors.New("invalid feed URL")

// Subscribe adds a new feed: writes to OPML and DB, then returns the new
// row. The caller can trigger an immediate poll if desired.
func (s *Service) Subscribe(ctx context.Context, feedURL, title, siteURL, category string) (*Feed, error) {
	feedURL = strings.TrimSpace(feedURL)
	u, err := url.Parse(feedURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrInvalidURL
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id, err := s.Store.UpsertFeed(ctx, &Feed{
		URL:      feedURL,
		Title:    title,
		SiteURL:  siteURL,
		Category: category,
	})
	if err != nil {
		return nil, err
	}
	if err := s.writeOPMLLocked(ctx); err != nil {
		return nil, err
	}
	feeds, err := s.Store.ListFeeds(ctx)
	if err != nil {
		return nil, err
	}
	for i := range feeds {
		if feeds[i].ID == id {
			return &feeds[i], nil
		}
	}
	return nil, errors.New("feed inserted but not found")
}

// Unsubscribe removes a feed by URL from both OPML and DB. Items are
// removed via ON DELETE CASCADE.
func (s *Service) Unsubscribe(ctx context.Context, feedURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Store.DeleteFeedByURL(ctx, feedURL); err != nil {
		return err
	}
	return s.writeOPMLLocked(ctx)
}

func (s *Service) writeOPMLLocked(ctx context.Context) error {
	feeds, err := s.Store.ListFeeds(ctx)
	if err != nil {
		return err
	}
	subs := make([]Subscription, len(feeds))
	for i, f := range feeds {
		subs[i] = Subscription{
			Title:    f.Title,
			FeedURL:  f.URL,
			SiteURL:  f.SiteURL,
			Category: f.Category,
		}
	}
	return SaveOPML(s.opmlPath, s.siteTitle, subs)
}
