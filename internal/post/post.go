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
	Date     time.Time `yaml:"date"`
	Tags     []string  `yaml:"tags,omitempty"`
	Body     string    `yaml:"-"`
	Filename string    `yaml:"-"`
}

func (p *Post) IsNote() bool { return p.Title == "" }

// Path returns the public URL path (no host).
func (p *Post) Path() string {
	if p.IsNote() {
		return "/notes/" + p.ID
	}
	return fmt.Sprintf("/%04d/%02d/%02d/%s", p.Date.Year(), p.Date.Month(), p.Date.Day(), slugify(p.Title))
}

func (p *Post) RenderHTML() (string, error) {
	var buf bytes.Buffer
	if err := goldmark.New().Convert([]byte(p.Body), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Excerpt returns a short text-only preview for index listings.
func (p *Post) Excerpt(max int) string {
	body := strings.TrimSpace(p.Body)
	if len(body) <= max {
		return body
	}
	return body[:max] + "…"
}

// Store loads, lists, and writes posts. Single-user scale: all posts live in memory.
type Store struct {
	dir string

	mu    sync.RWMutex
	byID  map[string]*Post
	order []*Post // newest first
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
		order = append(order, p)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].Date.After(order[j].Date) })

	s.mu.Lock()
	s.byID = byID
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
	for _, p := range s.order {
		if p.IsNote() {
			continue
		}
		if p.Date.Year() == year && int(p.Date.Month()) == month && p.Date.Day() == day && slugify(p.Title) == slug {
			return p, true
		}
	}
	return nil, false
}

// Create writes a new post to disk and adds it to the in-memory index.
func (s *Store) Create(title, body string, tags []string) (*Post, error) {
	p := &Post{
		ID:    newID(),
		Title: strings.TrimSpace(title),
		Date:  time.Now(),
		Tags:  tags,
		Body:  body,
	}
	p.Filename = filenameFor(p)
	if err := writeFile(filepath.Join(s.dir, p.Filename), p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.byID[p.ID] = p
	s.order = append([]*Post{p}, s.order...)
	s.mu.Unlock()
	return p, nil
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
		Date  time.Time `yaml:"date"`
		Tags  []string  `yaml:"tags,omitempty"`
	}{p.ID, p.Title, p.Date, p.Tags})
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
	stem := p.ID
	if !p.IsNote() {
		stem = slugify(p.Title)
	}
	return fmt.Sprintf("%s-%s.md", p.Date.Format("2006-01-02"), stem)
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
