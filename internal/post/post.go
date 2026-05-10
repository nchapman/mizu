package post

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"gopkg.in/yaml.v3"
)

// Post is the in-memory representation of a single entry on disk.
// Notes (no Title) and articles (with Title) share this type.
type Post struct {
	ID       string    `yaml:"id"`
	Title    string    `yaml:"title,omitempty"`
	Slug     string    `yaml:"slug,omitempty"` // frozen at create time; stable across title edits
	Date     time.Time `yaml:"date"`
	Tags     []string  `yaml:"tags,omitempty"`
	Body     string    `yaml:"-"`
	Filename string    `yaml:"-"`
}

func (p *Post) IsNote() bool { return p.Title == "" }

// effectiveSlug returns the stored slug, falling back to a derivation
// from the current title for posts written before the Slug field
// existed. New posts always have Slug populated, so the fallback is
// only exercised on legacy content.
func (p *Post) effectiveSlug() string {
	if p.Slug != "" {
		return p.Slug
	}
	return slugify(p.Title)
}

// Path returns the public URL path (no host).
func (p *Post) Path() string {
	if p.IsNote() {
		return "/notes/" + p.ID
	}
	return fmt.Sprintf("/%04d/%02d/%02d/%s", p.Date.Year(), p.Date.Month(), p.Date.Day(), p.effectiveSlug())
}

func (p *Post) RenderHTML() (string, error) {
	return renderMarkdown(p.Body)
}

// defaultMD is the shared goldmark instance every RenderHTML call goes
// through. Constructing a new goldmark.Markdown for each render
// allocates the parser and renderer rule set from scratch — a
// non-trivial cost when the public-site pipeline renders hundreds of
// posts per build. goldmark.Markdown.Convert is documented as
// concurrent-safe (v1.5+; we're on v1.7+), so the package-level
// instance is safe to share.
//
// Do NOT pass `html.WithUnsafe()` or other raw-HTML renderers without
// also adding a bluemonday sanitization pass on the way out — the
// output is consumed via `dangerouslySetInnerHTML` and `template.HTML`,
// both of which bypass escaping. The default goldmark config drops
// raw HTML; there's a regression test pinning that.
var defaultMD = goldmark.New()

func renderMarkdown(body string) (string, error) {
	var buf bytes.Buffer
	if err := defaultMD.Convert([]byte(body), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Excerpt returns a short text-only preview for index listings.
// max is a rune count, not a byte count, so we don't slice mid-codepoint.
func (p *Post) Excerpt(max int) string {
	body := strings.TrimSpace(p.Body)
	runes := []rune(body)
	if len(runes) <= max {
		return body
	}
	return string(runes[:max]) + "…"
}

// Store loads, lists, and writes posts and drafts. Single-user scale:
// everything lives in memory. Drafts and published posts are tracked
// in separate maps because they have separate URL spaces and lifecycle
// rules — a draft has no public URL until it's published.
//
// Reload caches per-file mtimes so a no-change reload (the common case
// when a watcher fires for an unrelated tree) skips parsing entirely.
// At a thousand posts that's the difference between ~50 ms of YAML
// work and a handful of stat syscalls.
type Store struct {
	dir      string // posts directory
	draftDir string

	mu         sync.RWMutex
	byID       map[string]*Post
	bySlug     map[string]*Post // key: "YYYY/MM/DD/slug" — articles only
	order      []*Post          // newest first
	drafts     map[string]*Draft
	draftIdx   []*Draft // newest-Created first
	postFiles  map[string]fileCache
	draftFiles map[string]fileCache
}

// fileCache pins the mtime + size + parsed value for a single .md so
// reload can stat-and-skip when nothing changed. Size is part of the
// key because mtime resolution is filesystem-dependent (1 second on
// NFS or some overlayfs setups) and git-restore-mtime workflows can
// preserve a prior mtime across content changes; combining the two
// catches every realistic edit short of a same-size content swap
// with a forged mtime.
type fileCache struct {
	modTime time.Time
	size    int64
	post    *Post  // populated for posts dir
	draft   *Draft // populated for drafts dir
}

func slugKey(p *Post) string {
	return fmt.Sprintf("%04d/%02d/%02d/%s", p.Date.Year(), p.Date.Month(), p.Date.Day(), p.effectiveSlug())
}

func NewStore(contentDir string) (*Store, error) {
	s := &Store{
		dir:      filepath.Join(contentDir, "posts"),
		draftDir: filepath.Join(contentDir, "drafts"),
	}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Dirs returns the on-disk paths backing this store. Used by callers
// like the filesystem watcher that need to subscribe to changes.
func (s *Store) Dirs() (postsDir, draftsDir string) {
	return s.dir, s.draftDir
}

// Reload re-reads all posts and drafts from disk, replacing the
// in-memory indexes atomically. Safe for concurrent readers — they
// may observe pre-reload pointers briefly until the swap, but they
// won't see a torn index. Used by the filesystem watcher when the
// operator edits markdown files outside the admin UI.
func (s *Store) Reload() error {
	return s.reload()
}

// reload re-reads everything from disk under the write lock. The
// disk reads happen inside the lock so a concurrent Create/Update/
// Publish can't write to the old maps and then have its insert
// silently dropped when reload swaps in fresh ones.
//
// Files whose mtime matches the last reload's snapshot are reused
// without re-parsing. At single-user scale even a full re-parse runs
// in tens of ms; the cache mostly saves work when a no-op signal
// fires (e.g. a media upload triggers reload but no .md changed).
func (s *Store) reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	byID := make(map[string]*Post)
	bySlug := make(map[string]*Post)
	var order []*Post
	newCache := make(map[string]fileCache, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", e.Name(), err)
		}
		modTime := info.ModTime()
		size := info.Size()
		var p *Post
		if cached, ok := s.postFiles[e.Name()]; ok && cached.post != nil && cached.modTime.Equal(modTime) && cached.size == size {
			p = cached.post
		} else {
			p, err = readFile(filepath.Join(s.dir, e.Name()))
			if err != nil {
				return fmt.Errorf("read %s: %w", e.Name(), err)
			}
		}
		newCache[e.Name()] = fileCache{modTime: modTime, size: size, post: p}
		byID[p.ID] = p
		if !p.IsNote() {
			bySlug[slugKey(p)] = p
		}
		order = append(order, p)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].Date.After(order[j].Date) })

	drafts, draftIdx, draftCache, err := s.loadDrafts()
	if err != nil {
		return err
	}

	s.byID = byID
	s.bySlug = bySlug
	s.order = order
	s.drafts = drafts
	s.draftIdx = draftIdx
	s.postFiles = newCache
	s.draftFiles = draftCache
	return nil
}

func (s *Store) loadDrafts() (map[string]*Draft, []*Draft, map[string]fileCache, error) {
	entries, err := os.ReadDir(s.draftDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]*Draft{}, nil, map[string]fileCache{}, nil
		}
		return nil, nil, nil, err
	}
	byID := make(map[string]*Draft)
	var order []*Draft
	newCache := make(map[string]fileCache, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("stat draft %s: %w", e.Name(), err)
		}
		modTime := info.ModTime()
		size := info.Size()
		var d *Draft
		if cached, ok := s.draftFiles[e.Name()]; ok && cached.draft != nil && cached.modTime.Equal(modTime) && cached.size == size {
			d = cached.draft
		} else {
			d, err = readDraftFile(filepath.Join(s.draftDir, e.Name()))
			if err != nil {
				return nil, nil, nil, fmt.Errorf("read draft %s: %w", e.Name(), err)
			}
		}
		newCache[e.Name()] = fileCache{modTime: modTime, size: size, draft: d}
		byID[d.ID] = d
		order = append(order, d)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].Created.After(order[j].Created) })
	return byID, order, newCache, nil
}

func (s *Store) Recent(limit int) []*Post {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.order) {
		limit = len(s.order)
	}
	out := make([]*Post, limit)
	copy(out, s.order[:limit])
	return out
}

// Page returns one page of posts in reverse-chronological order plus
// the total post count, computed atomically under a single lock so the
// page slice and total can never disagree.
//
// page is 1-indexed; page <= 0 is treated as 1. perPage <= 0 is treated
// as 1. An empty slice with total > 0 means the caller asked for a page
// past the end and should 404.
func (s *Store) Page(page, perPage int) (posts []*Post, total int) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 1
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	total = len(s.order)
	start := (page - 1) * perPage
	if start >= total {
		return nil, total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	out := make([]*Post, end-start)
	copy(out, s.order[start:end])
	return out, total
}

// Before returns up to limit posts strictly older than `t`, in the same
// reverse-chronological order as Recent. Pass the zero time to start from
// the newest post (equivalent to Recent for the first page). Used by the
// unified stream handler to paginate own posts alongside feed items.
func (s *Store) Before(t time.Time, limit int) []*Post {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		return nil
	}
	out := make([]*Post, 0, limit)
	for _, p := range s.order {
		if !t.IsZero() && !p.Date.Before(t) {
			continue
		}
		out = append(out, p)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (s *Store) ByID(id string) (*Post, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.byID[id]
	return p, ok
}

// BySlug finds an article by its date components and slug.
func (s *Store) BySlug(year, month, day int, slug string) (*Post, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.bySlug[fmt.Sprintf("%04d/%02d/%02d/%s", year, month, day, slug)]
	return p, ok
}

// ErrSlugTaken is returned when a new article's date+slug would collide with
// an existing post's URL. The caller should change the title.
var ErrSlugTaken = errors.New("post with this date and slug already exists")

// Create writes a new post to disk and adds it to the in-memory index.
// The lock is held across the file write: a duplicate Create racing in
// would otherwise pass the slug-taken check before the first write
// committed and produce two posts at the same URL. Single-user scale
// makes the I/O latency irrelevant.
func (s *Store) Create(title, body string, tags []string) (*Post, error) {
	p := &Post{
		ID:    newID(),
		Title: strings.TrimSpace(title),
		Date:  time.Now(),
		Tags:  tags,
		Body:  body,
	}
	if !p.IsNote() {
		// Freeze the slug at create time so future edits to the title
		// don't shift the URL underneath any links to this post.
		p.Slug = slugify(p.Title)
	}
	p.Filename = filenameFor(p)

	s.mu.Lock()
	defer s.mu.Unlock()
	if !p.IsNote() {
		if _, taken := s.bySlug[slugKey(p)]; taken {
			return nil, ErrSlugTaken
		}
	}
	if err := writeFile(filepath.Join(s.dir, p.Filename), p); err != nil {
		return nil, err
	}
	s.byID[p.ID] = p
	if !p.IsNote() {
		s.bySlug[slugKey(p)] = p
	}
	s.order = append([]*Post{p}, s.order...)
	return p, nil
}

// ErrNotFound is returned by Update/Delete when the post id doesn't
// exist in the store.
var ErrNotFound = errors.New("post not found")

// ErrTypeToggle is returned by Update when the edit would change a
// post between note and article shape, which would change its URL.
var ErrTypeToggle = errors.New("cannot toggle a post between note and article via edit")

// ErrDraftOrphan is returned by Publish alongside a valid *Post when
// the post was written successfully but the draft file could not be
// removed afterward. The post is live; the draft is an orphan that
// the operator can delete by hand. Callers should treat this as a
// success result with a warning, not a failure.
var ErrDraftOrphan = errors.New("publish succeeded but draft file remains")

// Update edits an existing post in place. The slug, ID, date, and
// filename are preserved — only title, body, and tags can change.
// This is deliberate: the URL must not move out from under existing
// links, and a stale "edit" that refers to an old date+slug must not
// silently retarget a different post.
//
// Posts written before the Slug field existed have an empty Slug
// when loaded; this method burns in a frozen slug derived from the
// pre-edit title, so the URL stays stable from the first edit
// onward (rather than silently drifting if the title is changed).
func (s *Store) Update(id, title, body string, tags []string) (*Post, error) {
	s.mu.Lock()
	p, ok := s.byID[id]
	if !ok {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	updated := *p
	updated.Title = strings.TrimSpace(title)
	updated.Body = body
	updated.Tags = tags
	if updated.IsNote() != p.IsNote() {
		s.mu.Unlock()
		return nil, ErrTypeToggle
	}
	if !updated.IsNote() && updated.Slug == "" {
		// Legacy post: freeze the slug from the *current* (pre-edit)
		// title so the URL doesn't shift underneath existing links.
		updated.Slug = slugify(p.Title)
	}
	filename := p.Filename
	s.mu.Unlock()

	if err := writeFile(filepath.Join(s.dir, filename), &updated); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// A concurrent Delete may have removed this post while we were
	// writing. Re-check before splicing so we don't resurrect a
	// deleted entry into a stale pointer.
	current, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	*current = updated
	return current, nil
}

// Delete removes a post from disk and the in-memory index. The
// caller is expected to handle webmention deletion notifications
// separately.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	p, ok := s.byID[id]
	if !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	filename := p.Filename
	s.mu.Unlock()

	if err := os.Remove(filepath.Join(s.dir, filename)); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.mu.Lock()
	delete(s.byID, id)
	if !p.IsNote() {
		delete(s.bySlug, slugKey(p))
	}
	for i, op := range s.order {
		if op.ID == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	return nil
}

// --- drafts ---

// Draft is an unpublished entry. It has no public URL — date and slug
// are decided at publish time, not draft creation, so the URL reflects
// when the post actually went live.
type Draft struct {
	ID       string    `yaml:"id"`
	Title    string    `yaml:"title,omitempty"`
	Tags     []string  `yaml:"tags,omitempty"`
	Created  time.Time `yaml:"created"`
	Body     string    `yaml:"-"`
	Filename string    `yaml:"-"`
}

func (d *Draft) RenderHTML() (string, error) {
	return renderMarkdown(d.Body)
}

func (s *Store) ListDrafts() []*Draft {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Draft, len(s.draftIdx))
	copy(out, s.draftIdx)
	return out
}

func (s *Store) DraftByID(id string) (*Draft, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.drafts[id]
	return d, ok
}

func (s *Store) CreateDraft(title, body string, tags []string) (*Draft, error) {
	d := &Draft{
		ID:      newID(),
		Title:   strings.TrimSpace(title),
		Tags:    tags,
		Created: time.Now(),
		Body:    body,
	}
	d.Filename = d.ID + ".md"

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeDraftFile(filepath.Join(s.draftDir, d.Filename), d); err != nil {
		return nil, err
	}
	s.drafts[d.ID] = d
	s.draftIdx = append([]*Draft{d}, s.draftIdx...)
	return d, nil
}

func (s *Store) UpdateDraft(id, title, body string, tags []string) (*Draft, error) {
	s.mu.Lock()
	d, ok := s.drafts[id]
	if !ok {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	updated := *d
	updated.Title = strings.TrimSpace(title)
	updated.Body = body
	updated.Tags = tags
	filename := d.Filename
	s.mu.Unlock()

	if err := writeDraftFile(filepath.Join(s.draftDir, filename), &updated); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.drafts[id]
	if !ok {
		return nil, ErrNotFound
	}
	*current = updated
	return current, nil
}

func (s *Store) DeleteDraft(id string) error {
	s.mu.Lock()
	d, ok := s.drafts[id]
	if !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	filename := d.Filename
	s.mu.Unlock()

	if err := os.Remove(filepath.Join(s.draftDir, filename)); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.mu.Lock()
	delete(s.drafts, id)
	for i, dd := range s.draftIdx {
		if dd.ID == id {
			s.draftIdx = append(s.draftIdx[:i], s.draftIdx[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	return nil
}

// Publish promotes a draft into a post. Date is set to now and the
// slug is frozen from the title (for articles). The draft file is
// removed only after the post file is written successfully — if the
// post write fails the draft remains intact for retry.
func (s *Store) Publish(id string) (*Post, error) {
	s.mu.Lock()
	d, ok := s.drafts[id]
	if !ok {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	p := &Post{
		ID:    d.ID, // keep the same id so internal references survive
		Title: d.Title,
		Date:  time.Now(),
		Tags:  d.Tags,
		Body:  d.Body,
	}
	if !p.IsNote() {
		p.Slug = slugify(p.Title)
	}
	p.Filename = filenameFor(p)
	if !p.IsNote() {
		if _, taken := s.bySlug[slugKey(p)]; taken {
			s.mu.Unlock()
			return nil, ErrSlugTaken
		}
	}
	if err := writeFile(filepath.Join(s.dir, p.Filename), p); err != nil {
		s.mu.Unlock()
		return nil, err
	}

	// Post is on disk; now reap the draft. A failure here is logged
	// upstream — the post is still valid, the draft just becomes an
	// orphan the operator can clean up by hand.
	draftPath := filepath.Join(s.draftDir, d.Filename)
	rmErr := os.Remove(draftPath)

	delete(s.drafts, id)
	for i, dd := range s.draftIdx {
		if dd.ID == id {
			s.draftIdx = append(s.draftIdx[:i], s.draftIdx[i+1:]...)
			break
		}
	}
	s.byID[p.ID] = p
	if !p.IsNote() {
		s.bySlug[slugKey(p)] = p
	}
	s.order = append([]*Post{p}, s.order...)
	s.mu.Unlock()

	if rmErr != nil && !os.IsNotExist(rmErr) {
		return p, fmt.Errorf("%w: %v", ErrDraftOrphan, rmErr)
	}
	return p, nil
}

func readDraftFile(path string) (*Draft, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := frontmatterRE.FindSubmatch(b)
	if m == nil {
		return nil, errors.New("missing frontmatter")
	}
	var d Draft
	if err := yaml.Unmarshal(m[1], &d); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	d.Body = string(m[2])
	d.Filename = filepath.Base(path)
	if d.ID == "" {
		return nil, errors.New("missing id")
	}
	return &d, nil
}

func writeDraftFile(path string, d *Draft) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	fm, err := yaml.Marshal(struct {
		ID      string    `yaml:"id"`
		Title   string    `yaml:"title,omitempty"`
		Tags    []string  `yaml:"tags,omitempty"`
		Created time.Time `yaml:"created"`
	}{d.ID, d.Title, d.Tags, d.Created})
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(fm)
	buf.WriteString("---\n\n")
	buf.WriteString(d.Body)
	if !strings.HasSuffix(d.Body, "\n") {
		buf.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- file I/O ---

// frontmatterRE matches a YAML frontmatter block followed by the
// body. After the closing `---` we consume up to two newlines: the
// fence's own line break plus the conventional blank-line separator.
// We deliberately don't consume more than that — extra leading blank
// lines in a body are author intent (poems, deliberate spacing) and
// must round-trip unchanged.
var frontmatterRE = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n?\r?\n?(.*)`)

func readFile(path string) (*Post, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := frontmatterRE.FindSubmatch(b)
	if m == nil {
		return nil, errors.New("missing frontmatter")
	}
	var p Post
	if err := yaml.Unmarshal(m[1], &p); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	p.Body = string(m[2])
	p.Filename = filepath.Base(path)
	if p.ID == "" {
		return nil, errors.New("missing id")
	}
	return &p, nil
}

func writeFile(path string, p *Post) error {
	fm, err := yaml.Marshal(struct {
		ID    string    `yaml:"id"`
		Title string    `yaml:"title,omitempty"`
		Slug  string    `yaml:"slug,omitempty"`
		Date  time.Time `yaml:"date"`
		Tags  []string  `yaml:"tags,omitempty"`
	}{p.ID, p.Title, p.Slug, p.Date, p.Tags})
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(fm)
	buf.WriteString("---\n\n")
	buf.WriteString(p.Body)
	if !strings.HasSuffix(p.Body, "\n") {
		buf.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func filenameFor(p *Post) string {
	if p.IsNote() {
		return fmt.Sprintf("%s-%s.md", p.Date.Format("2006-01-02"), p.ID)
	}
	// Articles include a short ID suffix so distinct posts that slugify to the
	// same string on the same day don't overwrite each other.
	return fmt.Sprintf("%s-%s-%s.md", p.Date.Format("2006-01-02"), p.effectiveSlug(), p.ID[:4])
}

// --- helpers ---

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRE.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func newID() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}
