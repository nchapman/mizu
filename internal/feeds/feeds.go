package feeds

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"

	"github.com/nchapman/mizu/internal/safehttp"
)

// Service is the public face of the feeds package — it keeps the OPML file
// and SQLite store in sync as subscriptions change. Mutating operations
// take a write lock so concurrent admin requests can't reorder OPML writes.
type Service struct {
	Store     *Store
	opmlPath  string
	siteTitle string

	// validate is the URL validator used by Subscribe. Tests swap this
	// out so they can subscribe to httptest servers (loopback) without
	// tripping the SSRF guard.
	validate func(ctx context.Context, raw string) (string, error)

	// mu serializes mutation of the (DB, OPML) pair so concurrent
	// subscribe/unsubscribe requests can't interleave OPML writes with
	// DB updates. RLock allows concurrent reads (ListFeeds) without
	// blocking each other.
	mu sync.RWMutex
}

func NewService(store *Store, opmlPath, siteTitle string) *Service {
	return &Service{Store: store, opmlPath: opmlPath, siteTitle: siteTitle, validate: validateFeedURL}
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

var (
	ErrInvalidURL     = errors.New("invalid feed URL")
	ErrBlockedAddress = errors.New("feed URL resolves to a blocked address")
)

// validateFeedURL ensures the URL is HTTP(S) and that its hostname
// resolves entirely to public addresses. The poller's HTTP client
// re-checks at dial time to catch DNS rebinding and redirect chains.
func validateFeedURL(ctx context.Context, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", ErrInvalidURL
	}
	host := u.Hostname()
	// Literal IP — check directly without DNS.
	if ip := net.ParseIP(host); ip != nil {
		if safehttp.IsBlockedIP(ip) {
			return "", ErrBlockedAddress
		}
		return raw, nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, ip := range ips {
		if safehttp.IsBlockedIP(ip) {
			return "", ErrBlockedAddress
		}
	}
	return raw, nil
}

// Subscribe adds a new feed. OPML is written before the DB upsert
// commits to ensure the durable source of truth is updated first; if
// the OPML write fails, the DB stays untouched.
func (s *Service) Subscribe(ctx context.Context, feedURL, title, siteURL, category string) (*Feed, error) {
	feedURL, err := s.validate(ctx, feedURL)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Build the prospective subscription list (existing + new) and write
	// OPML first. If this fails, nothing else changes.
	existing, err := s.Store.ListFeeds(ctx)
	if err != nil {
		return nil, err
	}
	prospective := make([]Subscription, 0, len(existing)+1)
	already := false
	for _, f := range existing {
		if f.URL == feedURL {
			// Re-subscribe with possibly new title/category — overwrite metadata.
			already = true
			prospective = append(prospective, Subscription{
				Title: orDefault(title, f.Title), FeedURL: f.URL,
				SiteURL: orDefault(siteURL, f.SiteURL), Category: orDefault(category, f.Category),
			})
			continue
		}
		prospective = append(prospective, Subscription{
			Title: f.Title, FeedURL: f.URL, SiteURL: f.SiteURL, Category: f.Category,
		})
	}
	if !already {
		prospective = append(prospective, Subscription{
			Title: title, FeedURL: feedURL, SiteURL: siteURL, Category: category,
		})
	}
	if err := SaveOPML(s.opmlPath, s.siteTitle, prospective); err != nil {
		return nil, fmt.Errorf("write opml: %w", err)
	}

	id, err := s.Store.UpsertFeed(ctx, &Feed{
		URL: feedURL, Title: title, SiteURL: siteURL, Category: category,
	})
	if err != nil {
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

// Unsubscribe removes a feed by URL. OPML write happens first so the
// source of truth is updated even if the DB delete races with a poll.
func (s *Service) Unsubscribe(ctx context.Context, feedURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.Store.ListFeeds(ctx)
	if err != nil {
		return err
	}
	prospective := make([]Subscription, 0, len(existing))
	for _, f := range existing {
		if f.URL == feedURL {
			continue
		}
		prospective = append(prospective, Subscription{
			Title: f.Title, FeedURL: f.URL, SiteURL: f.SiteURL, Category: f.Category,
		})
	}
	if err := SaveOPML(s.opmlPath, s.siteTitle, prospective); err != nil {
		return fmt.Errorf("write opml: %w", err)
	}
	return s.Store.DeleteFeedByURL(ctx, feedURL)
}

// ListFeeds returns the current subscription list under a read lock so
// callers can't observe a partially-mutated state mid-Subscribe.
func (s *Service) ListFeeds(ctx context.Context) ([]Feed, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Store.ListFeeds(ctx)
}

func orDefault(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
