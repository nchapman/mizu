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
	var buf bytes.Buffer
	if err := goldmark.New().Convert([]byte(p.Body), &buf); err != nil {
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

// Store loads, lists, and writes posts. Single-user scale: all posts live in memory.
type Store struct {
	dir string

	mu     sync.RWMutex
	byID   map[string]*Post
	bySlug map[string]*Post // key: "YYYY/MM/DD/slug" — articles only
	order  []*Post          // newest first
}

func slugKey(p *Post) string {
	return fmt.Sprintf("%04d/%02d/%02d/%s", p.Date.Year(), p.Date.Month(), p.Date.Day(), p.effectiveSlug())
}

func NewStore(contentDir string) (*Store, error) {
	s := &Store{dir: filepath.Join(contentDir, "posts")}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) reload() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	byID := make(map[string]*Post)
	bySlug := make(map[string]*Post)
	var order []*Post
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p, err := readFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return fmt.Errorf("read %s: %w", e.Name(), err)
		}
		byID[p.ID] = p
		if !p.IsNote() {
			bySlug[slugKey(p)] = p
		}
		order = append(order, p)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].Date.After(order[j].Date) })

	s.mu.Lock()
	s.byID = byID
	s.bySlug = bySlug
	s.order = order
	s.mu.Unlock()
	return nil
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

// --- file I/O ---

var frontmatterRE = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n?(.*)`)

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
