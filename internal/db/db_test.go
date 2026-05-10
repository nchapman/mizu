package db

import (
	"path/filepath"
	"testing"
)

func TestOpen_AppliesV1Schema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Open applies every embedded migration, so the recorded version
	// tracks the latest file in migrations/. We assert "at least 1"
	// rather than a specific number so adding migrations doesn't churn
	// this test — the per-table assertions below cover what we actually
	// care about (the v1 schema is fully present).
	v, err := readVersion(conn)
	if err != nil {
		t.Fatal(err)
	}
	if v < 1 {
		t.Fatalf("schema_version=%d, want >= 1", v)
	}

	// schema_version row is in app_meta, not PRAGMA user_version, so
	// the bump commits atomically with the DDL.
	var stored string
	if err := conn.QueryRow(`SELECT value FROM app_meta WHERE key='schema_version'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "" {
		t.Errorf("app_meta schema_version is empty")
	}

	// Every table from 0001_init must be present.
	for _, table := range []string{
		"users", "sessions", "login_attempts", "app_meta",
		"feeds", "items", "mentions",
	} {
		var name string
		err := conn.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}

	// Foreign keys must be enforced (DSN sets the pragma).
	var fk int
	if err := conn.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys=%d, want 1", fk)
	}
}

func TestMigrate_IdempotentOnRerun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Insert a sentinel row so we can detect if Migrate accidentally
	// re-ran 0001_init (CREATE TABLE would have failed, but if the
	// runner ever swallowed errors the row would survive while the
	// table got blown away — explicit assertion is cheaper than trust).
	if _, err := conn.Exec(`INSERT INTO app_meta(key, value) VALUES('canary', 'v1')`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(conn); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	var got string
	if err := conn.QueryRow(`SELECT value FROM app_meta WHERE key='canary'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "v1" {
		t.Errorf("canary survived = %q, want v1", got)
	}
}

func TestParseMigrationName(t *testing.T) {
	cases := []struct {
		in      string
		wantVer int
		wantOK  bool
	}{
		{"0001_init.sql", 1, true},
		{"0042_add_thing.sql", 42, true},
		{"not_a_number.sql", 0, false},
		{"_missing_prefix.sql", 0, false},
		{"0000_zero.sql", 0, false}, // versions start at 1
	}
	for _, c := range cases {
		// strip the ".sql" the way the loader does
		name := c.in
		if !endsWith(name, ".sql") {
			name = c.in + ".sql"
		}
		v, _, ok := parseMigrationName(name)
		if ok != c.wantOK || (ok && v != c.wantVer) {
			t.Errorf("parseMigrationName(%q)=(%d,%v), want (%d,%v)", c.in, v, ok, c.wantVer, c.wantOK)
		}
	}
}

func endsWith(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}
