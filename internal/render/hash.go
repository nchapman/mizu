package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// HashState is the persisted index of last-written-output content
// hashes. Loading it on startup lets a clean restart skip every file
// whose intended bytes match what's already on disk.
type HashState struct {
	path    string
	Hashes  map[string]string `json:"hashes"`
	Version int               `json:"version"`
}

// loadHashState reads state/build.json. A missing file is not an error;
// it just yields an empty state, which forces a full first build.
func loadHashState(path string) (*HashState, error) {
	s := &HashState{path: path, Hashes: map[string]string{}, Version: 1}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, s); err != nil {
		// Corrupt hash file means the next build rewrites everything;
		// log and recover rather than refusing to run.
		s.Hashes = map[string]string{}
		return s, nil
	}
	if s.Hashes == nil {
		s.Hashes = map[string]string{}
	}
	s.path = path
	return s, nil
}

func (s *HashState) save() error {
	if s.path == "" {
		return nil
	}
	// Sort keys so the file is stable across builds — easier diffs.
	type entry struct {
		K, V string
	}
	entries := make([]entry, 0, len(s.Hashes))
	for k, v := range s.Hashes {
		entries = append(entries, entry{k, v})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].K < entries[j].K })
	sorted := make(map[string]string, len(entries))
	for _, e := range entries {
		sorted[e.K] = e.V
	}
	b, err := json.MarshalIndent(struct {
		Version int               `json:"version"`
		Hashes  map[string]string `json:"hashes"`
	}{s.Version, sorted}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	// 0o600: build.json lists every output path, including draft slugs.
	// A co-tenant or backup agent reading state/build.json would learn
	// every draft URL without needing to guess the HMAC slug.
	return writeSecret(s.path, b)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
