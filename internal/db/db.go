// Package db owns the single SQLite file that holds every piece of
// persistent state mizu cares about: user accounts and sessions,
// fetched feed items and read state, received webmentions, and the
// per-install draft salt.
//
// Other packages (auth, feeds, webmention, render) share the *sql.DB
// returned by Open. Schema lives in migrations/*.sql and is applied
// in order via PRAGMA user_version, so post-release schema changes
// only require dropping a new file in the migrations dir.
package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open returns a *sql.DB pointing at path, ready for use by every
// service. WAL is on for concurrent reads during a write; foreign
// keys are enforced so ON DELETE CASCADE works; a busy timeout
// absorbs the small writer-contention windows that show up on a
// busy server. Migrations are applied automatically.
func Open(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("db path is empty")
	}
	// `file:` prefix lets SQLite parse the path itself; `?_pragma=` is
	// modernc's way to set PRAGMAs at open time. We leave synchronous
	// at WAL's default of NORMAL: every commit fsyncs the WAL, and the
	// only data loss window is an OS crash or power cut between commit
	// and the next checkpoint. FULL would double-fsync on every write
	// for a microblog's traffic — not the right tradeoff here.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// The pool is capped at one connection on purpose. SQLite serializes
	// writers regardless of pool size, and WAL mode buys us concurrent
	// readers — but several invariants in internal/auth (notably the
	// lockout counter in checkLock/recordFailure) assume that read +
	// write are serialized at the Go side. If you raise this cap, audit
	// those flows for concurrent-failure interleavings and switch the
	// write transactions to BEGIN IMMEDIATE first. For a single-operator
	// instance the throughput cost of one connection is invisible.
	conn.SetMaxOpenConns(1)

	if err := Migrate(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
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
