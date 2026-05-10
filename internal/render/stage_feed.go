package render

import (
	"bytes"
	"context"
	"fmt"

	"github.com/gorilla/feeds"
)

// FeedLimit caps the RSS feed at this many recent posts. Feed readers
// only need a recent slice; emitting the full archive bloats every poll.
const FeedLimit = 50

// FeedStage emits feed.xml.
type FeedStage struct{}

func (FeedStage) Name() string { return "feed" }

func (FeedStage) Build(_ context.Context, snap *Snapshot) ([]Output, error) {
	feed := &feeds.Feed{
		Title:       snap.Site.Title,
		Link:        &feeds.Link{Href: snap.BaseURL},
		Description: snap.Site.Description,
	}
	// Feed-level <lastBuildDate> is derived from the most recent post's
	// date rather than time.Now() — otherwise every build produces a new
	// byte sequence and the pipeline's content-hash skip can never fire
	// on feed.xml.
	if len(snap.Posts) > 0 {
		feed.Created = snap.Posts[0].Date
	}
	limit := len(snap.Posts)
	if limit > FeedLimit {
		limit = FeedLimit
	}
	for _, p := range snap.Posts[:limit] {
		html, ok := snap.PostHTML[p.ID]
		if !ok {
			var err error
			html, err = p.RenderHTML()
			if err != nil {
				return nil, fmt.Errorf("render %s: %w", p.ID, err)
			}
		}
		title := p.Title
		if title == "" {
			title = p.Excerpt(80)
		}
		feed.Items = append(feed.Items, &feeds.Item{
			Id:          p.ID,
			Title:       title,
			Link:        &feeds.Link{Href: snap.BaseURL + p.Path()},
			Description: html,
			Created:     p.Date,
		})
	}
	var buf bytes.Buffer
	if err := feed.WriteRss(&buf); err != nil {
		return nil, fmt.Errorf("write rss: %w", err)
	}
	return []Output{{Path: "feed.xml", Body: buf.Bytes()}}, nil
}
