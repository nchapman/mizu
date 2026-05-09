package webmention

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger appends one JSON record per webmention event to a JSONL file.
// The DB is the queryable index; the log is the durable archive that
// can rebuild the DB if it's lost or corrupted.
type Logger struct {
	mu   sync.Mutex
	path string
}

func NewLogger(stateDir string) (*Logger, error) {
	if err := ensureDir(stateDir); err != nil {
		return nil, err
	}
	return &Logger{path: filepath.Join(stateDir, "webmentions.log.jsonl")}, nil
}

type LogEntry struct {
	At        time.Time `json:"at"`
	Direction string    `json:"direction"` // "received" or "sent"
	Source    string    `json:"source"`
	Target    string    `json:"target"`
	Status    Status    `json:"status"`
	Error     string    `json:"error,omitempty"`
}

func (l *Logger) Append(e LogEntry) error {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

// Replay reads every entry in the log in order and calls fn for each.
// Used by an admin tool / future repair path; not on the hot path.
func (l *Logger) Replay(fn func(LogEntry) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*64), 1024*1024) // allow up to 1MiB per line
	for scanner.Scan() {
		var e LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
