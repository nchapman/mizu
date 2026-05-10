package webmention

import (
	"context"
	"database/sql"
	"errors"
	"time"

	mizudb "github.com/nchapman/mizu/internal/db"
)

// Status describes where a mention is in its lifecycle.
type Status string

const (
	StatusPending  Status = "pending"  // received, not yet verified
	StatusVerified Status = "verified" // source confirmed to link to target
	StatusRejected Status = "rejected" // source did not link to target, or was unreachable
	StatusRemoved  Status = "removed"  // source no longer contains the link (deletion per spec)
)

// Store reads and writes the mentions table on the shared *sql.DB.
// Schema is managed centrally by internal/db.
type Store struct {
	db *mizudb.DB
}

// NewStore wires a Store onto an already-open, already-migrated DB.
func NewStore(db *mizudb.DB) *Store { return &Store{db: db} }

// Upsert inserts a new mention or updates the status of an existing
// (source, target) pair. The (source, target) UNIQUE constraint
// dedupes resends of the same notification.
func (s *Store) Upsert(ctx context.Context, m Mention) error {
	var verifiedAt sql.NullInt64
	if m.Status == StatusVerified && !m.VerifiedAt.IsZero() {
		verifiedAt = sql.NullInt64{Int64: m.VerifiedAt.Unix(), Valid: true}
	}
	_, err := s.db.W.ExecContext(ctx, `
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
	rows, err := s.db.R.QueryContext(ctx, `
		SELECT id, source, target, status, received_at, verified_at, last_error
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

// Recent returns the most recent verified mentions across all targets,
// newest first. Used by the admin to surface incoming activity. limit
// is clamped to 1..200.
func (s *Store) Recent(ctx context.Context, limit int) ([]Mention, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.R.QueryContext(ctx, `
		SELECT id, source, target, status, received_at, verified_at, last_error
		FROM mentions
		WHERE status = ?
		ORDER BY COALESCE(verified_at, received_at) DESC
		LIMIT ?
	`, string(StatusVerified), limit)
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

// AllVerified returns every verified mention across all targets,
// newest verified first. Used by the render pipeline to populate
// per-post mention lists in a single query rather than one query
// per post page.
func (s *Store) AllVerified(ctx context.Context) ([]Mention, error) {
	rows, err := s.db.R.QueryContext(ctx, `
		SELECT id, source, target, status, received_at, verified_at, last_error
		FROM mentions
		WHERE status = ?
		ORDER BY COALESCE(verified_at, received_at) DESC
	`, string(StatusVerified))
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

// CountPending returns the number of (source, target) pairs in the
// pending state. Used to bound queue depth at the receive endpoint
// so a flood of novel pairs can't grow the mentions table or the
// verifier backlog without limit.
func (s *Store) CountPending(ctx context.Context) (int, error) {
	var n int
	err := s.db.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM mentions WHERE status = ?`,
		string(StatusPending),
	).Scan(&n)
	return n, err
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
	rows, err := s.db.R.QueryContext(ctx,
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

// SetLastError stamps a fresh error message onto an existing
// (source, target) row without changing its status. Used by the
// verifier when a fetch fails transiently — the row stays Pending so
// the next startup retries, but the latest error text is captured for
// the operator to inspect.
//
// Scoped to rows still in the pending state so a late-arriving
// transient error can't clobber the message on a row that has since
// been re-submitted and reset by Upsert.
func (s *Store) SetLastError(ctx context.Context, source, target, msg string) error {
	_, err := s.db.W.ExecContext(ctx,
		`UPDATE mentions SET last_error = ?
		 WHERE source = ? AND target = ? AND status = ?`,
		msg, source, target, string(StatusPending))
	return err
}

// ErrNotFound is returned when looking up a mention by id finds nothing.
var ErrNotFound = errors.New("mention not found")
