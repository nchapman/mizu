package webmention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Status describes where a mention is in its lifecycle.
type Status string

const (
	StatusPending  Status = "pending"  // received, not yet verified
	StatusVerified Status = "verified" // source confirmed to link to target
	StatusRejected Status = "rejected" // source did not link to target, or was unreachable
	StatusRemoved  Status = "removed"  // source no longer contains the link (deletion per spec)
)

const schema = `
CREATE TABLE IF NOT EXISTS mentions (
  id           INTEGER PRIMARY KEY,
  source       TEXT NOT NULL,
  target       TEXT NOT NULL,
  status       TEXT NOT NULL,
  received_at  INTEGER NOT NULL,
  verified_at  INTEGER,
  last_error   TEXT,
  UNIQUE(source, target)
);

CREATE INDEX IF NOT EXISTS mentions_target_idx ON mentions(target, status);
`

// Store persists received mentions. The DB is regeneratable from the
// JSONL log (see Logger), so a corrupted DB can be rebuilt without
// data loss.
type Store struct {
	db *sql.DB
}

func OpenStore(cacheDir string) (*Store, error) {
	if err := ensureDir(cacheDir); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(cacheDir, "webmentions.db") + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Upsert inserts a new mention or updates the status of an existing
// (source, target) pair. The (source, target) UNIQUE constraint
// dedupes resends of the same notification.
func (s *Store) Upsert(ctx context.Context, m Mention) error {
	var verifiedAt sql.NullInt64
	if m.Status == StatusVerified && !m.VerifiedAt.IsZero() {
		verifiedAt = sql.NullInt64{Int64: m.VerifiedAt.Unix(), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO mentions (source, target, status, received_at, verified_at, last_error)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(source, target) DO UPDATE SET
			status      = excluded.status,
			verified_at = excluded.verified_at,
			last_error  = excluded.last_error
	`, m.Source, m.Target, string(m.Status), m.ReceivedAt.Unix(), verifiedAt, m.LastError)
	return err
}

type Mention struct {
	ID         int64
	Source     string
	Target     string
	Status     Status
	ReceivedAt time.Time
	VerifiedAt time.Time
	LastError  string
}

// ForTarget returns the verified mentions for a given target URL,
// newest first. Pending and rejected mentions are filtered out so the
// public render path never shows unverified user-supplied URLs.
func (s *Store) ForTarget(ctx context.Context, target string) ([]Mention, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source, target, status, received_at, verified_at, COALESCE(last_error, '')
		FROM mentions
		WHERE target = ? AND status = ?
		ORDER BY COALESCE(verified_at, received_at) DESC
	`, target, string(StatusVerified))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mention
	for rows.Next() {
		var m Mention
		var status string
		var receivedAt int64
		var verifiedAt sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Source, &m.Target, &status, &receivedAt, &verifiedAt, &m.LastError); err != nil {
			return nil, err
		}
		m.Status = Status(status)
		m.ReceivedAt = time.Unix(receivedAt, 0)
		if verifiedAt.Valid {
			m.VerifiedAt = time.Unix(verifiedAt.Int64, 0)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PendingPair is a (source, target) row that's waiting for verification.
type PendingPair struct {
	Source string
	Target string
}

// Pending returns every (source, target) pair currently in the
// pending state. Used at startup to re-queue work that the previous
// process didn't finish — for example, mentions received just before
// shutdown, or jobs dropped when the in-memory queue was full.
func (s *Store) Pending(ctx context.Context) ([]PendingPair, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT source, target FROM mentions WHERE status = ?`,
		string(StatusPending))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingPair
	for rows.Next() {
		var p PendingPair
		if err := rows.Scan(&p.Source, &p.Target); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ErrNotFound is returned when looking up a mention by id finds nothing.
var ErrNotFound = errors.New("mention not found")
