package admin

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/nchapman/repeat/internal/feeds"
	"github.com/nchapman/repeat/internal/post"
)

// streamItemDTO is a tagged-union JSON entry for the unified stream.
// Exactly one of Item or Post is populated, matching Kind.
type streamItemDTO struct {
	Kind string           `json:"kind"` // "feed" | "own"
	Item *timelineItemDTO `json:"item,omitempty"`
	Post *postDTO         `json:"post,omitempty"`
}

type streamResponse struct {
	Items      []streamItemDTO `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// streamCursor splits pagination across the two sources. A page boundary
// is described by the last feed item we returned (Feed) and the date of
// the last own post we returned (PostBefore). Either side may be empty
// when one source dominates a page; the next request just keeps using
// that side's previous cursor.
type streamCursor struct {
	Feed       feeds.TimelineCursor
	PostBefore time.Time
}

func (c streamCursor) IsZero() bool {
	return c.Feed.IsZero() && c.PostBefore.IsZero()
}

type streamCursorWire struct {
	FT int64 `json:"ft,omitempty"` // feed published_at unix
	FI int64 `json:"fi,omitempty"` // feed item id
	P  int64 `json:"p,omitempty"`  // post-before unix
}

func encodeStreamCursor(c streamCursor) string {
	if c.IsZero() {
		return ""
	}
	var w streamCursorWire
	if !c.Feed.PublishedAt.IsZero() {
		w.FT = c.Feed.PublishedAt.Unix()
	}
	w.FI = c.Feed.ID
	if !c.PostBefore.IsZero() {
		w.P = c.PostBefore.Unix()
	}
	b, _ := json.Marshal(w)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeStreamCursor(s string) (streamCursor, bool) {
	if s == "" {
		return streamCursor{}, true
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return streamCursor{}, false
	}
	var w streamCursorWire
	if err := json.Unmarshal(b, &w); err != nil {
		return streamCursor{}, false
	}
	var c streamCursor
	if w.FT > 0 {
		c.Feed.PublishedAt = time.Unix(w.FT, 0)
	}
	c.Feed.ID = w.FI
	if w.P > 0 {
		c.PostBefore = time.Unix(w.P, 0)
	}
	return c, true
}

// stream returns a chronological mix of the operator's own posts and
// items from feeds they subscribe to. Filter values:
//   - "" or "all": both sources, all read state
//   - "unread":    feed items with read_at IS NULL only (no own posts)
//   - "following": feed items only (no own posts), all read state
//   - "yours":     own posts only
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cursor, ok := decodeStreamCursor(q.Get("cursor"))
	if !ok {
		http.Error(w, "bad cursor", http.StatusBadRequest)
		return
	}
	filter := q.Get("filter")

	wantFeeds := filter == "" || filter == "all" || filter == "unread" || filter == "following"
	wantPosts := filter == "" || filter == "all" || filter == "yours"
	unreadOnly := filter == "unread"

	var feedItems []feeds.Item
	if wantFeeds {
		items, err := s.feeds.Store.Timeline(r.Context(), cursor.Feed, limit, unreadOnly)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		feedItems = items
	}
	var posts []*post.Post
	if wantPosts {
		posts = s.posts.Before(cursor.PostBefore, limit)
	}

	out := make([]streamItemDTO, 0, limit)
	var lastFeed *feeds.Item
	var lastPost *post.Post
	fi, pi := 0, 0
	for len(out) < limit {
		if fi >= len(feedItems) && pi >= len(posts) {
			break
		}
		// Pick whichever side has the newer head item; tie-break feed
		// first so equal-timestamp pages remain deterministic.
		var pickFeed bool
		switch {
		case fi < len(feedItems) && pi < len(posts):
			pickFeed = !feedItems[fi].PublishedAt.Before(posts[pi].Date)
		case fi < len(feedItems):
			pickFeed = true
		default:
			pickFeed = false
		}
		if pickFeed {
			it := feedItems[fi]
			fi++
			lastFeed = &it
			out = append(out, streamItemDTO{Kind: "feed", Item: feedItemToDTO(it)})
			continue
		}
		p := posts[pi]
		pi++
		lastPost = p
		dto, err := toDTO(p)
		if err != nil {
			log.Printf("admin render post %s: %v", p.ID, err)
			http.Error(w, "render failed", http.StatusInternalServerError)
			return
		}
		out = append(out, streamItemDTO{Kind: "own", Post: &dto})
	}

	resp := streamResponse{Items: out}
	if len(out) == limit {
		next := cursor
		if lastFeed != nil {
			next.Feed = feeds.TimelineCursor{PublishedAt: lastFeed.PublishedAt, ID: lastFeed.ID}
		}
		if lastPost != nil {
			next.PostBefore = lastPost.Date
		}
		resp.NextCursor = encodeStreamCursor(next)
	}
	writeJSON(w, http.StatusOK, resp)
}

func feedItemToDTO(it feeds.Item) *timelineItemDTO {
	d := &timelineItemDTO{
		ID: it.ID, FeedID: it.FeedID, FeedTitle: it.FeedTitle,
		URL: it.URL, Title: it.Title, Author: it.Author, Content: it.Content,
		Read: it.ReadAt != nil,
	}
	if !it.PublishedAt.IsZero() {
		d.PublishedAt = it.PublishedAt.Format(time.RFC3339)
	}
	return d
}
