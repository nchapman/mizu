// Package db owns the single SQLite file that holds every piece of
// persistent state mizu cares about: user accounts and sessions,
// fetched feed items and read state, received webmentions, and the
// per-install draft salt.
//
// Open returns a *DB that wraps two connection pools to the same
// underlying file: a single-connection writer (so writes serialize at
// the Go layer with no SQLITE_BUSY churn) and a multi-connection
// reader (so WAL's concurrent-read story actually pays out). Services
// pick the right handle for the call: writes/transactions go through
// W, read-only queries go through R. Schema lives in migrations/*.sql
// and is applied in order via PRAGMA user_version, so post-release
// schema changes only require dropping a new file in the migrations
// dir.
package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB groups the two SQLite connection pools the rest of the app uses.
// SQLite serializes writers at the file level regardless of pool size,
// so the writer pool is capped at one connection; WAL mode lets many
// readers run concurrently against committed snapshots, so the reader
// pool is sized to the box. Splitting the pools means a long write
// (e.g. a feed poll committing dozens of item inserts in one tx)
// doesn't queue up admin reads behind it.
//
// Both handles point at the same database file. Callers should treat
// W as the source of truth for read-after-write semantics inside a
// single transaction, and use R for everything else.
type DB struct {
	// W is the writer pool, capped at one open connection so the auth
	// lockout flow's read-then-write sequences can rely on no other
	// writer interleaving. Opened with _txlock=immediate so every
	// BeginTx grabs the write lock at BEGIN — without this, a
	// transaction that starts with a SELECT and later UPDATEs has to
	// upgrade its lock mid-flight and can lose the race to another
	// writer (returning SQLITE_BUSY).
	W *sql.DB
	// R is the reader pool. Opened with query_only so any accidental
	// write through this handle is rejected at the SQLite layer
	// instead of being silently routed to the wrong pool.
	R *sql.DB
}

// Open opens both pools, applies pending migrations against the
// writer, and returns the wrapper. The reader is opened after
// migrations so it sees a fully-built schema on its very first query.
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, fmt.Errorf("db path is empty")
	}
	// Shared pragmas: WAL for concurrent reads during a write;
	// foreign_keys so ON DELETE CASCADE works; busy_timeout absorbs
	// the brief windows where a reader trips over a checkpointer or
	// the writer holds the write lock at commit. synchronous stays at
	// WAL's default of NORMAL — every commit fsyncs the WAL, and the
	// only data-loss window is an OS crash between commit and the
	// next checkpoint. FULL would double-fsync every write for a
	// microblog's traffic — not the right tradeoff.
	common := "_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"

	writer, err := sql.Open("sqlite", "file:"+path+"?"+common+"&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open sqlite writer: %w", err)
	}
	writer.SetMaxOpenConns(1)

	if err := Migrate(writer); err != nil {
		_ = writer.Close()
		return nil, err
	}

	reader, err := sql.Open("sqlite", "file:"+path+"?"+common+"&_pragma=query_only(1)")
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("open sqlite reader: %w", err)
	}
	n := runtime.NumCPU()
	if n < 4 {
		n = 4
	}
	reader.SetMaxOpenConns(n)

	return &DB{W: writer, R: reader}, nil
}

// Close closes both pools. The writer is closed last so any final
// reader cleanup that races shutdown can still see committed state.
func (d *DB) Close() error {
	rerr := d.R.Close()
	werr := d.W.Close()
	if werr != nil {
		return werr
	}
	return rerr
}

// Migrate brings the database up to the latest schema version by
// applying every embedded migration whose number is greater than the
// version recorded in app_meta. Each migration runs inside its own
// transaction, and every migration is responsible for bumping the
// schema_version row inside that same transaction so the version bump
// is atomic with the DDL. A crash mid-migration leaves the previous
// version in place; the next startup re-runs the failed migration
// from scratch.
//
// Migration files live in migrations/ and follow NNNN_name.sql.
// Zero-padded prefixes make lexical sort match numeric order.
func Migrate(conn *sql.DB) error {
	current, err := readVersion(conn)
	if err != nil {
		return err
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := applyMigration(conn, m); err != nil {
			return fmt.Errorf("migration %04d (%s): %w", m.version, m.name, err)
		}
	}
	return nil
}

type migration struct {
	version int
	name    string
	body    string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		ver, name, ok := parseMigrationName(e.Name())
		if !ok {
			return nil, fmt.Errorf("migration filename %q must look like NNNN_name.sql", e.Name())
		}
		body, err := fs.ReadFile(migrationsFS, filepath.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: ver, name: name, body: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %d", out[i].version)
		}
	}
	return out, nil
}

func parseMigrationName(name string) (int, string, bool) {
	base := strings.TrimSuffix(name, ".sql")
	parts := strings.SplitN(base, "_", 2)
	if len(parts) == 0 {
		return 0, "", false
	}
	ver, err := strconv.Atoi(parts[0])
	if err != nil || ver <= 0 {
		return 0, "", false
	}
	short := ""
	if len(parts) == 2 {
		short = parts[1]
	}
	return ver, short, true
}

func applyMigration(conn *sql.DB, m migration) error {
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(m.body); err != nil {
		_ = tx.Rollback()
		return err
	}
	// Bump the recorded version inside the same transaction as the DDL
	// so the version write commits atomically with the schema change.
	// Migration files must NOT write the schema_version row themselves —
	// this is the single, uniform place that owns the bump.
	if _, err := tx.Exec(
		`INSERT INTO app_meta(key, value) VALUES('schema_version', ?)
		   ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		fmt.Sprintf("%d", m.version),
	); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// readVersion returns the recorded schema version, or 0 if app_meta
// doesn't exist yet (fresh DB) or the row hasn't been written.
func readVersion(conn *sql.DB) (int, error) {
	// Probe for the table; modernc.org/sqlite returns "no such table"
	// for a fresh DB and we treat that as version 0.
	var name string
	err := conn.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='app_meta'`,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("probe app_meta: %w", err)
	}
	var v string
	err = conn.QueryRow(`SELECT value FROM app_meta WHERE key='schema_version'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read schema_version: %w", err)
	}
	n, perr := strconv.Atoi(v)
	if perr != nil {
		return 0, fmt.Errorf("parse schema_version %q: %w", v, perr)
	}
	return n, nil
}
