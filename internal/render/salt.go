package render

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	mizudb "github.com/nchapman/mizu/internal/db"
)

// draftSaltKey is the row in app_meta that stores the per-install
// secret used to derive unguessable draft preview slugs.
const draftSaltKey = "draft_salt"

// LoadOrCreateDraftSalt returns the per-install secret used to derive
// unguessable draft preview slugs. The salt lives in the app_meta
// table; the first call on a fresh DB generates 32 random bytes and
// inserts them. Subsequent calls return the stored value.
//
// Stored hex-encoded for human inspection if the operator queries the
// table directly. Decode failures are treated as corruption and the
// caller gets an error rather than a silently regenerated salt — a
// quietly rotated salt would invalidate every outstanding draft URL.
func LoadOrCreateDraftSalt(ctx context.Context, db *mizudb.DB) ([]byte, error) {
	if salt, ok, err := readSalt(ctx, db.R); err != nil {
		return nil, err
	} else if ok {
		return salt, nil
	}

	// Generate and insert. INSERT OR IGNORE handles the race between
	// two callers seeing an empty table simultaneously — whichever
	// wins, both end up returning the same salt on the next read.
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("generate draft salt: %w", err)
	}
	encoded := hex.EncodeToString(fresh)
	if _, err := db.W.ExecContext(ctx,
		`INSERT OR IGNORE INTO app_meta(key, value) VALUES(?, ?)`,
		draftSaltKey, encoded,
	); err != nil {
		return nil, fmt.Errorf("insert draft salt: %w", err)
	}
	// Re-read so the loser of the insert race ends up with the same
	// bytes as the winner. Read via the writer pool: the row was just
	// committed and we want strict read-after-write semantics here
	// without relying on the reader pool's snapshot timing.
	salt, ok, err := readSalt(ctx, db.W)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("draft salt missing after insert")
	}
	return salt, nil
}

func readSalt(ctx context.Context, conn *sql.DB) ([]byte, bool, error) {
	var value string
	err := conn.QueryRowContext(ctx,
		`SELECT value FROM app_meta WHERE key = ?`, draftSaltKey,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read draft salt: %w", err)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) < 16 {
		return nil, false, fmt.Errorf("draft salt in app_meta is corrupt (len=%d, decode err=%v)", len(decoded), err)
	}
	return decoded, true, nil
}
