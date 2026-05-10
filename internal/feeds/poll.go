package feeds

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/mmcdole/gofeed"

	"github.com/nchapman/mizu/internal/safehttp"
)

// MaxFeedBodyBytes caps the response body of a single feed fetch. A
// hostile or runaway feed server could otherwise stream gigabytes into
// memory before the parser returns.
const MaxFeedBodyBytes = 10 << 20 // 10 MB

// Poller periodically fetches every subscribed feed, using conditional GET
// (etag / last-modified) to avoid re-downloading unchanged content, and
// inserts new items into the store.
type Poller struct {
	store     *Store
	interval  time.Duration
	userAgent string
	parser    *gofeed.Parser
	http      *http.Client
	sanitizer *bluemonday.Policy
}

func NewPoller(s *Store, interval time.Duration, userAgent string) *Poller {
	return &Poller{
		store:     s,
		interval:  interval,
		userAgent: userAgent,
		parser:    gofeed.NewParser(),
		http:      safehttp.NewClient(),
		sanitizer: bluemonday.UGCPolicy(),
	}
}

// Run polls on startup, then every interval, until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	p.PollAll(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.PollAll(ctx)
		}
	}
}

// PollAll fetches all known feeds sequentially. Sequential is fine at
// single-user scale and avoids hammering smaller sites.
func (p *Poller) PollAll(ctx context.Context) {
	feeds, err := p.store.ListFeeds(ctx)
	if err != nil {
		log.Printf("poll: list feeds: %v", err)
		return
	}
	for _, f := range feeds {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := p.PollOne(ctx, f); err != nil {
			log.Printf("poll %s: %v", f.URL, err)
			_ = p.store.MarkFetchError(ctx, f.ID, err.Error())
		}
	}
}

func (p *Poller) PollOne(ctx context.Context, f Feed) error {
	req, err := http.NewRequestWithContext(ctx, "GET", f.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", p.userAgent)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/json, application/xml;q=0.9, */*;q=0.8")
	if f.ETag != "" {
		req.Header.Set("If-None-Match", f.ETag)
	}
	if f.LastModified != "" {
		req.Header.Set("If-Modified-Since", f.LastModified)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return p.store.MarkFetched(ctx, f.ID, f.ETag, f.LastModified, "", "")
	}
	if resp.StatusCode >= 400 {
		// Drain a little of the body for context, then bail.
		_, _ = io.CopyN(io.Discard, resp.Body, 1<<10)
		return fmt.Errorf("http %d", resp.StatusCode)
	}

	parsed, err := p.parser.Parse(io.LimitReader(resp.Body, MaxFeedBodyBytes))
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	now := time.Now()
	for _, e := range parsed.Items {
		guid := e.GUID
		if guid == "" {
			guid = e.Link
		}
		if guid == "" {
			continue
		}
		published := time.Time{}
		if e.PublishedParsed != nil {
			published = *e.PublishedParsed
		} else if e.UpdatedParsed != nil {
			published = *e.UpdatedParsed
		}
		author := ""
		if e.Author != nil {
			author = e.Author.Name
		}
		content := e.Content
		if content == "" {
			content = e.Description
		}
		// Sanitize HTML at ingest so the timeline UI can render it safely.
		// UGCPolicy strips <script>, event handlers, javascript: URIs,
		// and other XSS vectors while preserving common formatting.
		content = p.sanitizer.Sanitize(content)
		if _, err := p.store.InsertItem(ctx, &Item{
			FeedID:      f.ID,
			GUID:        guid,
			URL:         e.Link,
			Title:       e.Title,
			Author:      author,
			Content:     content,
			PublishedAt: published,
			FetchedAt:   now,
		}); err != nil {
			return fmt.Errorf("insert item: %w", err)
		}
	}
	return p.store.MarkFetched(ctx, f.ID, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"), parsed.Title, parsed.Link)
}
