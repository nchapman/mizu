package render

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/nchapman/mizu/internal/db"
)

func TestLoadOrCreateDraftSalt_GeneratesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx := context.Background()
	salt1, err := LoadOrCreateDraftSalt(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(salt1) != 32 {
		t.Errorf("len=%d, want 32", len(salt1))
	}

	// Second call returns the same bytes.
	salt2, err := LoadOrCreateDraftSalt(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(salt1, salt2) {
		t.Error("salt changed on second call")
	}

	// Survives a close/reopen — the salt is durable.
	_ = conn.Close()
	conn2, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	salt3, err := LoadOrCreateDraftSalt(ctx, conn2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(salt1, salt3) {
		t.Error("salt changed across reopen")
	}
}

func TestLoadOrCreateDraftSalt_RejectsCorruptStored(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Hand-write a junk value into app_meta.
	if _, err := conn.W.Exec(`INSERT INTO app_meta(key, value) VALUES('draft_salt', 'not-hex')`); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateDraftSalt(context.Background(), conn); err == nil {
		t.Fatal("expected error on corrupt salt; got nil (silent regeneration would invalidate every outstanding draft URL)")
	}
}
