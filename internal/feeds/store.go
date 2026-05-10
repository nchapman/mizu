// Package feeds is the inbound side of mizu: feeds the user subscribes to,
// items fetched from those feeds, and read state.
//
// Two storage layers cooperate:
//
//   - subscriptions.opml on disk is the durable, portable source of truth
//     for the user's subscription list — easy to back up, easy to move
//     between instances.
//   - state/mizu.db (the shared SQLite owned by internal/db) holds the
//     fetched items and read state. Schema for the feeds/items tables
//     lives in db/migrations/.
package feeds

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Feed struct {
	ID            int64
	URL           string
	Title         string
	SiteURL       string
	Category      string
	ETag          string
	LastModified  string
	LastFetchedAt time.Time
	LastError     string
}

type Item struct {
	ID          int64
	FeedID      int64
	FeedTitle   string
	GUID        string
	URL         string
	Title       string
	Author      string
	Content     string
	PublishedAt time.Time
	FetchedAt   time.Time
	ReadAt      *time.Time
}

// Store reads and writes the feeds/items tables on the shared *sql.DB.
// Schema is managed centrally by internal/db; we don't own the
// connection here, so Close is a no-op.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore wires a Store onto an already-open, already-migrated DB.
func NewStore(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }

// UpsertFeed inserts or updates a feed by URL, preserving fetch metadata
// (etag, last_modified) on conflict so an OPML re-import doesn't force a
// full re-fetch.
func (s *Store) UpsertFeed(ctx context.Context, f *Feed) (int64, error) {
	const q = `
INSERT INTO feeds (url, title, site_url, category)
VALUES (?, ?, ?, ?)
ON CONFLICT(url) DO UPDATE SET
  title    = COALESCE(NULLIF(excluded.title, ''), feeds.title),
  site_url = COALESCE(NULLIF(excluded.site_url, ''), feeds.site_url),
  category = excluded.category
RETURNING id`
	var id int64
	err := s.db.QueryRowContext(ctx, q, f.URL, f.Title, f.SiteURL, f.Category).Scan(&id)
	return id, err
}

func (s *Store) DeleteFeedByURL(ctx context.Context, url string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM feeds WHERE url = ?`, url)
	return err
}

func (s *Store) ListFeeds(ctx context.Context) ([]Feed, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, url, title, site_url, category, etag, last_modified,
       COALESCE(last_fetched_at, 0), last_error
FROM feeds ORDER BY title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Feed
	for rows.Next() {
		var f Feed
		var fetched int64
		if err := rows.Scan(&f.ID, &f.URL, &f.Title, &f.SiteURL, &f.Category,
			&f.ETag, &f.LastModified, &fetched, &f.LastError); err != nil {
			return nil, err
		}
		if fetched > 0 {
			f.LastFetchedAt = time.Unix(fetched, 0)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// MarkFetched updates fetch metadata after a successful poll. parsedTitle
// and parsedSiteURL come from the feed body (channel <title>, channel <link>);
// they fill in empty slots only, so an operator-supplied title/site URL stays
// sticky — same precedence as UpsertFeed's ON CONFLICT clause.
func (s *Store) MarkFetched(ctx context.Context, feedID int64, etag, lastModified, parsedTitle, parsedSiteURL string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE feeds SET
  etag = ?,
  last_modified = ?,
  last_fetched_at = ?,
  last_error = '',
  title    = CASE WHEN title    = '' THEN ? ELSE title    END,
  site_url = CASE WHEN site_url = '' THEN ? ELSE site_url END
WHERE id = ?`, etag, lastModified, s.now().Unix(), parsedTitle, parsedSiteURL, feedID)
	return err
}

func (s *Store) MarkFetchError(ctx context.Context, feedID int64, msg string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE feeds SET last_fetched_at = ?, last_error = ? WHERE id = ?`,
		s.now().Unix(), msg, feedID)
	return err
}

// InsertItem inserts a single item, ignoring duplicates on (feed_id, guid).
// Returns true if the row was newly inserted.
func (s *Store) InsertItem(ctx context.Context, it *Item) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO items (feed_id, guid, url, title, author, content, published_at, fetched_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(feed_id, guid) DO NOTHING`,
		it.FeedID, it.GUID, it.URL, it.Title, it.Author, it.Content,
		nullableUnix(it.PublishedAt), it.FetchedAt.Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TimelineCursor is the page boundary for Timeline. It is composite —
// (published_at, id) — because many items can share a published_at,
// especially zero-time items. Using only published_at would skip or
// duplicate items across pages.
type TimelineCursor struct {
	PublishedAt time.Time
	ID          int64
}

func (c TimelineCursor) IsZero() bool { return c.ID == 0 && c.PublishedAt.IsZero() }

// Timeline returns items across all feeds, newest first. `before` is an
// exclusive composite cursor; pass the zero value for the first page.
func (s *Store) Timeline(ctx context.Context, before TimelineCursor, limit int, unreadOnly bool) ([]Item, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args := []any{}
	where := "1=1"
	if !before.IsZero() {
		// SQLite supports row-value comparison: (a, b) < (?, ?) is the
		// standard tuple-less-than that gives a correct strict ordering.
		var pub int64
		if !before.PublishedAt.IsZero() {
			pub = before.PublishedAt.Unix()
		}
		where += " AND (COALESCE(items.published_at, 0), items.id) < (?, ?)"
		args = append(args, pub, before.ID)
	}
	if unreadOnly {
		where += " AND items.read_at IS NULL"
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT items.id, items.feed_id, feeds.title, items.guid,
       items.url, items.title, items.author, items.content,
       COALESCE(items.published_at, 0), items.fetched_at, items.read_at
FROM items JOIN feeds ON feeds.id = items.feed_id
WHERE `+where+`
ORDER BY COALESCE(items.published_at, 0) DESC, items.id DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var pub, fetched int64
		var read sql.NullInt64
		if err := rows.Scan(&it.ID, &it.FeedID, &it.FeedTitle, &it.GUID, &it.URL,
			&it.Title, &it.Author, &it.Content, &pub, &fetched, &read); err != nil {
			return nil, err
		}
		if pub > 0 {
			it.PublishedAt = time.Unix(pub, 0)
		}
		it.FetchedAt = time.Unix(fetched, 0)
		if read.Valid {
			t := time.Unix(read.Int64, 0)
			it.ReadAt = &t
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ErrItemNotFound is returned by MarkRead when the item ID doesn't
// match any row, so handlers can map it to 404.
var ErrItemNotFound = errors.New("item not found")

func (s *Store) MarkRead(ctx context.Context, itemID int64, read bool) error {
	var res sql.Result
	var err error
	if read {
		res, err = s.db.ExecContext(ctx, `UPDATE items SET read_at = ? WHERE id = ?`, s.now().Unix(), itemID)
	} else {
		res, err = s.db.ExecContext(ctx, `UPDATE items SET read_at = NULL WHERE id = ?`, itemID)
	}
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrItemNotFound
	}
	return nil
}

// FeedFetchInfo returns the conditional-GET headers for the next poll.
func (s *Store) FeedFetchInfo(ctx context.Context, feedID int64) (etag, lastModified string, err error) {
	row := s.db.QueryRowContext(ctx, `SELECT etag, last_modified FROM feeds WHERE id = ?`, feedID)
	err = row.Scan(&etag, &lastModified)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

func nullableUnix(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}
