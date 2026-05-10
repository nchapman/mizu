package render

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Pipeline is the coordinator that turns sources into baked files
// under PublicDir. One Pipeline runs per process; Build is goroutine-
// safe. Use Run to start the background worker that drains Enqueue
// requests; Build is exposed for synchronous use in tests and for the
// admin "rebuild now" path.
type Pipeline struct {
	sources   *SnapshotSources
	stages    []Stage
	publicDir string
	hashPath  string
	debounce  time.Duration

	hashes *HashState

	mu      sync.Mutex // serializes Build calls; one builder at a time
	pending chan struct{}

	// LastBuild and LastError are populated after each build for the
	// admin UI to surface status. Read under statusMu.
	statusMu  sync.RWMutex
	lastBuild time.Time
	lastErr   error
}

// Options configures a Pipeline. Sources is required. PublicDir defaults
// to <stateDir>/public; HashPath to <stateDir>/build.json. Stages
// defaults to the standard set in DefaultStages().
type Options struct {
	Sources   *SnapshotSources
	PublicDir string
	HashPath  string
	Stages    []Stage
	Debounce  time.Duration
}

// DefaultStages returns the standard production stage set in the order
// the pipeline runs them.
func DefaultStages() []Stage {
	return []Stage{
		ThemeAssetStage{},
		ImageVariantStage{},
		PostPageStage{},
		IndexStage{},
		FeedStage{},
		SitemapStage{},
		RobotsStage{},
		DraftStage{},
	}
}

// NewPipeline constructs a Pipeline. The hash file is loaded eagerly so
// the first Build can skip files whose intended bytes are already on
// disk from a previous run.
func NewPipeline(opts Options) (*Pipeline, error) {
	if opts.Sources == nil {
		return nil, errors.New("render: Sources is required")
	}
	if opts.PublicDir == "" {
		return nil, errors.New("render: PublicDir is required")
	}
	if opts.HashPath == "" {
		return nil, errors.New("render: HashPath is required")
	}
	if opts.Debounce == 0 {
		// 500ms (rather than 200) gives truncate-and-write editors
		// time to finish flushing a multi-MB file before we snapshot
		// from disk. Atomic-rename editors (vim) coalesce to a single
		// event regardless of debounce; the cost of the higher window
		// is only paid on bursts.
		opts.Debounce = 500 * time.Millisecond
	}
	if len(opts.Stages) == 0 {
		opts.Stages = DefaultStages()
	}
	if err := os.MkdirAll(opts.PublicDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir public: %w", err)
	}
	hashes, err := loadHashState(opts.HashPath)
	if err != nil {
		return nil, fmt.Errorf("load hash state: %w", err)
	}
	return &Pipeline{
		sources:   opts.Sources,
		stages:    opts.Stages,
		publicDir: opts.PublicDir,
		hashPath:  opts.HashPath,
		debounce:  opts.Debounce,
		hashes:    hashes,
		pending:   make(chan struct{}, 1),
	}, nil
}

// Enqueue requests a build. Non-blocking, coalescing: a queued request
// while a build is running collapses into a single subsequent build.
func (p *Pipeline) Enqueue() {
	select {
	case p.pending <- struct{}{}:
	default: // already pending
	}
}

// Run drains Enqueue requests until ctx is cancelled. The first action
// is a full build so a freshly started process reconciles disk against
// current sources before serving any traffic.
func (p *Pipeline) Run(ctx context.Context) {
	if err := p.Build(ctx); err != nil {
		log.Printf("render: initial build: %v", err)
	}

	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.pending:
			timerC = time.After(p.debounce)
		case <-timerC:
			timerC = nil
			if err := p.Build(ctx); err != nil {
				log.Printf("render: build: %v", err)
			}
		}
	}
}

// Status returns the timestamp and error of the most recent completed
// build. Used by the admin UI to surface render health.
func (p *Pipeline) Status() (time.Time, error) {
	p.statusMu.RLock()
	defer p.statusMu.RUnlock()
	return p.lastBuild, p.lastErr
}

// Build runs every stage once and reconciles PublicDir with the
// produced output set. Errors from individual stages are logged and
// don't abort the pass — what *can* be written, is. If any stage
// errored, the orphan-file GC pass is skipped: deleting a file whose
// owning stage failed would silently lose pages until the stage
// recovers.
func (p *Pipeline) Build(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	snap, err := p.sources.Build(ctx)
	if err != nil {
		p.recordStatus(err)
		return err
	}

	type stagedOut struct {
		stage string
		out   Output
	}
	var produced []stagedOut
	var stageErrs []error
	for _, st := range p.stages {
		outs, err := st.Build(ctx, snap)
		if err != nil {
			stageErrs = append(stageErrs, fmt.Errorf("%s: %w", st.Name(), err))
			log.Printf("render: stage %s: %v", st.Name(), err)
		}
		for _, o := range outs {
			produced = append(produced, stagedOut{stage: st.Name(), out: o})
		}
	}

	// Detect duplicate paths across stages — a bug, but the pipeline
	// shouldn't silently overwrite. First write wins; later collisions
	// are logged and skipped.
	intended := map[string]string{} // path → stage that owns it
	newHashes := map[string]string{}
	for _, so := range produced {
		if owner, dup := intended[so.out.Path]; dup {
			log.Printf("render: %s wants to write %s, already owned by %s — skipping",
				so.stage, so.out.Path, owner)
			continue
		}
		intended[so.out.Path] = so.stage
		hash := sha256Hex(so.out.Body)
		abs := filepath.Join(p.publicDir, so.out.Path)
		if prev, ok := p.hashes.Hashes[so.out.Path]; ok && prev == hash {
			// Already on disk with these bytes — but if the file was
			// deleted out from under us we still need to write. Treat
			// the stat as load-bearing: don't refactor it away. The
			// hash-skip would otherwise hide a deleted output forever.
			if _, statErr := os.Stat(abs); statErr == nil {
				newHashes[so.out.Path] = hash
				continue
			}
		}
		if err := atomicWrite(abs, so.out.Body); err != nil {
			stageErrs = append(stageErrs, fmt.Errorf("write %s: %w", so.out.Path, err))
			log.Printf("render: write %s: %v", so.out.Path, err)
			// Don't record the hash for a write that didn't land —
			// otherwise the next build sees "hash matches, file
			// exists with stale bytes" and never tries again.
			continue
		}
		newHashes[so.out.Path] = hash
	}

	// GC orphan files. Skipped if any stage errored — we don't know
	// which paths the failing stages would have produced, and losing
	// them would feel like data loss to the operator.
	if len(stageErrs) == 0 {
		if err := p.gcOrphans(intended); err != nil {
			log.Printf("render: gc: %v", err)
		}
	}

	p.hashes.Hashes = newHashes
	if err := p.hashes.save(); err != nil {
		log.Printf("render: save hashes: %v", err)
	}

	if len(stageErrs) > 0 {
		err := errors.Join(stageErrs...)
		p.recordStatus(err)
		return err
	}
	p.recordStatus(nil)
	return nil
}

func (p *Pipeline) recordStatus(err error) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	p.lastBuild = time.Now()
	p.lastErr = err
}

// gcOrphans walks PublicDir and deletes any file not in intended.
// Empty directories are pruned bottom-up after their files go.
func (p *Pipeline) gcOrphans(intended map[string]string) error {
	var orphans []string
	err := filepath.WalkDir(p.publicDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(p.publicDir, path)
		if err != nil {
			return err
		}
		// Use forward slashes to match the keys produced by stages.
		key := filepath.ToSlash(rel)
		// Skip stray .tmp-* files left from a crash mid-write.
		if strings.Contains(filepath.Base(path), ".tmp-") {
			orphans = append(orphans, path)
			return nil
		}
		if _, ok := intended[key]; !ok {
			orphans = append(orphans, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, o := range orphans {
		if err := os.Remove(o); err != nil && !os.IsNotExist(err) {
			log.Printf("render: remove orphan %s: %v", o, err)
		}
	}
	// Prune empty directories.
	return pruneEmptyDirs(p.publicDir)
}

func pruneEmptyDirs(root string) error {
	// Collect dirs depth-first so children are evaluated before parents.
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Sort by descending path depth so children are removed before
	// their parents. Length-as-proxy would break if a shallow dir had
	// a longer name than a deeper one.
	sort.Slice(dirs, func(i, j int) bool {
		return strings.Count(dirs[i], string(os.PathSeparator)) > strings.Count(dirs[j], string(os.PathSeparator))
	})
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		if len(entries) == 0 {
			_ = os.Remove(d)
		}
	}
	return nil
}
