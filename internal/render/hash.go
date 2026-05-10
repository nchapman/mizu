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
// hashes plus the stage-defined input fingerprints used by stages
// that can skip expensive renders entirely. Loading it on startup
// lets a clean restart skip every file whose intended bytes match
// what's already on disk and lets stages like ImageVariantStage
// avoid decoding sources that haven't changed.
type HashState struct {
	path    string
	Hashes  map[string]string `json:"hashes"`
	Inputs  map[string]string `json:"inputs,omitempty"`
	Version int               `json:"version"`
}

// loadHashState reads state/build.json. A missing file is not an error;
// it just yields an empty state, which forces a full first build.
func loadHashState(path string) (*HashState, error) {
	s := &HashState{path: path, Hashes: map[string]string{}, Inputs: map[string]string{}, Version: 1}
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
		s.Inputs = map[string]string{}
		return s, nil
	}
	if s.Hashes == nil {
		s.Hashes = map[string]string{}
	}
	if s.Inputs == nil {
		s.Inputs = map[string]string{}
	}
	s.path = path
	return s, nil
}

func (s *HashState) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(struct {
		Version int               `json:"version"`
		Hashes  map[string]string `json:"hashes"`
		Inputs  map[string]string `json:"inputs,omitempty"`
	}{s.Version, sortMap(s.Hashes), sortMap(s.Inputs)}, "", "  ")
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

// sortMap returns a copy of m with keys iterated in sorted order. JSON
// marshaling of map[string]string is stable in Go, but we route through
// a sorted intermediate so the file diffs cleanly across builds even
// if the upstream behavior ever changes.
func sortMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return m
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(m))
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}
