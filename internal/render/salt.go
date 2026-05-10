package render

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// LoadOrCreateDraftSalt returns the per-install secret used to derive
// unguessable draft preview slugs. Persisted at <stateDir>/draft_salt
// with mode 0o600 — readable by mizu only. A missing file is treated
// as a fresh install: 32 random bytes are generated and written.
func LoadOrCreateDraftSalt(stateDir string) ([]byte, error) {
	path := filepath.Join(stateDir, "draft_salt")
	b, err := os.ReadFile(path)
	if err == nil {
		// Stored hex-encoded for readability if an operator inspects the
		// file. Decode failures are treated as corruption — regenerate
		// rather than serve predictable URLs.
		decoded, derr := hex.DecodeString(string(b))
		if derr == nil && len(decoded) >= 16 {
			return decoded, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	encoded := []byte(hex.EncodeToString(salt))
	// Write the salt with 0o600 mode from the start — the file's entire
	// security value is its secrecy, so a wider permission even for the
	// rename window is unacceptable. We bypass the shared atomicWrite
	// helper (which hardcodes 0o644) and inline a 0o600 version.
	if err := writeSecret(path, encoded); err != nil {
		return nil, err
	}
	return salt, nil
}

func writeSecret(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := path + ".tmp-" + hex.EncodeToString(rnd[:])
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
