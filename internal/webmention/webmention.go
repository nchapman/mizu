// Package webmention implements both sides of the W3C Webmention
// protocol: a /webmention receive endpoint with a background
// verification worker, and an outbound sender that scans published
// posts for links and notifies the linked sites.
//
// State lives in the shared SQLite database (mentions table) — that's
// the queryable index and the durable record. Schema lives in
// internal/db/migrations.
package webmention

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nchapman/mizu/internal/safehttp"
)

// Service ties the store and verifier together. One instance per
// process; safe for concurrent use.
type Service struct {
	store   *Store
	http    *http.Client
	baseURL string // e.g. "https://example.com" — used to validate inbound targets

	queue chan job // verification work

	// onChange is invoked when a mention reaches a terminal state
	// (Verified or Rejected). The render pipeline hooks this to
	// rebuild affected post pages: Verified→need to add the entry,
	// Rejected-after-Verified→need to remove the now-stale entry.
	// Pending and transient-error transitions don't fire — Pending
	// mentions are never rendered, so no observable state changes.
	// Read by the verifier goroutine, written by main wiring;
	// guarded by onChangeMu.
	onChangeMu sync.RWMutex
	onChange   func()
}

type job struct {
	source string
	target string
}

// New wires up the service. baseURL is the site's public origin; only
// targets within that origin are accepted by Receive.
func New(store *Store, baseURL string) *Service {
	return &Service{
		store:   store,
		http:    safehttp.NewClient(),
		baseURL: strings.TrimRight(baseURL, "/"),
		queue:   make(chan job, 256),
	}
}

// ErrBadTarget is returned by Receive when the target URL doesn't
// belong to this site. The caller should respond with 400.
var ErrBadTarget = errors.New("target is not on this site")

// ErrSameSource is returned when source and target are the same URL,
// which would let an attacker amplify their own content through us.
var ErrSameSource = errors.New("source and target must differ")

// ErrQueueFull is returned when the pending mention table has hit
// MaxPendingMentions. The caller should respond with 503; legitimate
// senders will retry per spec.
var ErrQueueFull = errors.New("webmention queue is full")

// MaxPendingMentions caps the number of pending (source, target) rows
// the receiver will accept before rejecting new ones. Without it a
// hostile sender (or an outage of the verifier) could grow the
// mentions table and verifier backlog without bound. The cap is
// generous enough that legitimate bursts (a popular post being
// linked widely) sail through.
const MaxPendingMentions = 1000

// Receive validates a (source, target) pair and queues it for
// verification. Returns synchronously; verification runs in the
// background worker.
func (s *Service) Receive(ctx context.Context, source, target string) error {
	if source == "" || target == "" {
		return errors.New("source and target are required")
	}
	if source == target {
		return ErrSameSource
	}
	su, err := url.Parse(source)
	if err != nil || (su.Scheme != "http" && su.Scheme != "https") {
		return errors.New("source must be an http(s) URL")
	}
	tu, err := url.Parse(target)
	if err != nil || (tu.Scheme != "http" && tu.Scheme != "https") {
		return errors.New("target must be an http(s) URL")
	}
	if !s.targetOnSite(target) {
		return ErrBadTarget
	}

	// Cap pending depth before any write so a flood of novel pairs
	// can't grow the table or the verifier backlog. The (source,
	// target) UNIQUE constraint already dedupes resends, so this only
	// gates net-new pairs.
	pending, err := s.store.CountPending(ctx)
	if err != nil {
		return fmt.Errorf("count pending: %w", err)
	}
	if pending >= MaxPendingMentions {
		return ErrQueueFull
	}

	m := Mention{
		Source:     source,
		Target:     target,
		Status:     StatusPending,
		ReceivedAt: time.Now().UTC(),
	}
	if err := s.store.Upsert(ctx, m); err != nil {
		return fmt.Errorf("store mention: %w", err)
	}

	// Non-blocking enqueue. If the queue is full the work is dropped
	// at the door — the spec allows asynchronous processing and the
	// sender is expected to retry. Logged so we notice if it happens.
	select {
	case s.queue <- job{source: source, target: target}:
	default:
		log.Printf("webmention: queue full, dropping %s -> %s", source, target)
	}
	return nil
}

func (s *Service) targetOnSite(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || s.baseURL == "" {
		return false
	}
	base, err := url.Parse(s.baseURL)
	if err != nil {
		return false
	}
	// Match scheme + host (case-insensitive, trailing-dot tolerant).
	// Path/query don't matter for the gate.
	return u.Scheme == base.Scheme && hostEqual(u.Host, base.Host)
}

func hostEqual(a, b string) bool {
	a = strings.TrimSuffix(strings.ToLower(a), ".")
	b = strings.TrimSuffix(strings.ToLower(b), ".")
	return a == b
}

// RunVerifier processes the queue until ctx is cancelled. Run as a
// goroutine on startup. Each job verifies that source actually links
// to target, updates the store, and writes a log entry.
//
// Before entering the main loop, any rows still at StatusPending are
// re-enqueued. Pending rows accumulate when (a) the previous process
// shut down before draining, or (b) the in-memory queue was full and
// a Receive call dropped its job. The store is the durable record;
// the channel is just a hot cache.
func (s *Service) RunVerifier(ctx context.Context) {
	pending, err := s.store.Pending(ctx)
	if err != nil {
		log.Printf("webmention: load pending on startup: %v", err)
	}
	for _, p := range pending {
		select {
		case s.queue <- job{source: p.Source, target: p.Target}:
		case <-ctx.Done():
			return
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.queue:
			s.processOne(ctx, j)
		}
	}
}

func (s *Service) processOne(ctx context.Context, j job) {
	// Independent timeout per job — we don't want a slow source to
	// block the worker indefinitely.
	jobCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	err := Verify(jobCtx, s.http, j.source, j.target)
	if err == nil {
		m := Mention{
			Source: j.source, Target: j.target,
			Status:     StatusVerified,
			ReceivedAt: time.Now().UTC(),
			VerifiedAt: time.Now().UTC(),
		}
		_ = s.store.Upsert(jobCtx, m)
		s.fireOnChange()
		return
	}

	// ErrLinkNotFound is a permanent rejection: the source page was
	// reachable and didn't link to us. Anything else (DNS failure,
	// timeout, 5xx) we treat as transient — leave the row at
	// StatusPending so a future startup or re-driver retries it.
	if errors.Is(err, ErrLinkNotFound) {
		m := Mention{
			Source: j.source, Target: j.target,
			Status:     StatusRejected,
			ReceivedAt: time.Now().UTC(),
			LastError:  err.Error(),
		}
		_ = s.store.Upsert(jobCtx, m)
		// Fire on rejection too: a previously-verified mention whose
		// source dropped the link must disappear from the rendered
		// page on the next build. The hash-skip absorbs the no-op
		// case where the mention was never verified to begin with.
		s.fireOnChange()
		return
	}
	// Transient: stash the latest error on the row but keep it pending
	// so RunVerifier's startup sweep retries on the next process start.
	_ = s.store.SetLastError(jobCtx, j.source, j.target, err.Error())
}

// ForTarget exposes the store's read API to handlers that render
// mention lists on post pages.
func (s *Service) ForTarget(ctx context.Context, target string) ([]Mention, error) {
	return s.store.ForTarget(ctx, target)
}

// AllVerified returns every verified mention. Used by the render
// pipeline to populate per-post mention lists in a single query.
func (s *Service) AllVerified(ctx context.Context) ([]Mention, error) {
	return s.store.AllVerified(ctx)
}

// OnMentionsChanged registers a callback invoked whenever a mention
// reaches a terminal state (Verified or Rejected). main.go wires this
// to Pipeline.Enqueue so post pages re-render with the new state —
// crucially, this includes the Verified→Rejected transition that
// happens when a previously-verified source removes the link and the
// sender re-notifies us. Pass nil to clear. Safe to call concurrently
// with the verifier worker.
func (s *Service) OnMentionsChanged(cb func()) {
	s.onChangeMu.Lock()
	s.onChange = cb
	s.onChangeMu.Unlock()
}

func (s *Service) fireOnChange() {
	s.onChangeMu.RLock()
	cb := s.onChange
	s.onChangeMu.RUnlock()
	if cb != nil {
		cb()
	}
}

// Recent passes through to the store. Used by the admin to list
// incoming verified mentions across every target.
func (s *Service) Recent(ctx context.Context, limit int) ([]Mention, error) {
	return s.store.Recent(ctx, limit)
}

// --- Sending ---

// Send notifies the webmention endpoint at endpoint that source has
// linked to target. Best-effort: returns an error but the caller is
// expected to log and move on rather than retry indefinitely.
func (s *Service) Send(ctx context.Context, endpoint, source, target string) error {
	form := url.Values{
		"source": {source},
		"target": {target},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		// Drain a bounded amount so the connection can be reused, but
		// don't let a hostile endpoint stream forever into us. The
		// 30s client timeout bounds wall-clock; this bounds memory.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("endpoint returned %d", resp.StatusCode)
	}
	return nil
}

// SendForPost discovers and notifies the webmention endpoint for every
// outbound link in the rendered HTML of a post. Each notification is a
// best-effort fire-and-forget; failures are logged, never propagated.
//
// sourceURL is the absolute URL of the post (the "source" we report);
// renderedHTML is the post's HTML body (we extract <a href> from it).
func (s *Service) SendForPost(ctx context.Context, sourceURL, renderedHTML string) {
	links := extractLinks(renderedHTML)
	for _, target := range links {
		// Skip self-links and same-origin links — sending mentions to
		// our own pages is wasteful and could amplify into loops.
		if target == sourceURL || s.targetOnSite(target) {
			continue
		}
		endpoint, err := Discover(ctx, s.http, target)
		if err != nil {
			if !errors.Is(err, ErrNoEndpoint) {
				log.Printf("webmention: discover %s: %v", target, err)
			}
			continue
		}
		if err := s.Send(ctx, endpoint, sourceURL, target); err != nil {
			log.Printf("webmention: send %s -> %s: %v", sourceURL, target, err)
			continue
		}
	}
}
