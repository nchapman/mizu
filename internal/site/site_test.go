package site

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nchapman/repeat/internal/config"
	"github.com/nchapman/repeat/internal/post"
	"github.com/nchapman/repeat/internal/webmention"
)

// minimal templates used by every site test. They cover all the blocks
// New() expects without depending on the real on-disk theme.
const (
	tplBase = `{{define "base"}}<!doctype html><html><head><title>{{block "title" .}}{{.Site.Title}}{{end}}</title></head><body>{{block "main" .}}{{end}}</body></html>{{end}}`
	tplIdx  = `{{template "base" .}}{{define "main"}}{{range .Posts}}<article data-id="{{.ID}}">{{if .Title}}<h2><a href="{{.Path}}">{{.Title}}</a></h2>{{else}}<a href="{{.Path}}">note</a>{{end}}<div class="body">{{.HTML}}</div></article>{{else}}<p>none</p>{{end}}{{end}}`
	tplPost = `{{template "base" .}}{{define "main"}}<article>{{if .Post.Title}}<h2>{{.Post.Title}}</h2>{{end}}<div class="body">{{.Post.HTML}}</div></article>{{if .Mentions}}<ul>{{range .Mentions}}<li>{{hostOf .Source}}</li>{{end}}</ul>{{end}}{{end}}`
)

// newSite builds a fully wired site Server backed by tempdir templates,
// posts, and a webmention service. The returned router has Routes
// registered.
func newSite(t *testing.T) (*Server, http.Handler, *post.Store, *webmention.Service) {
	t.Helper()
	dir := t.TempDir()
	tplDir := filepath.Join(dir, "tpl")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"base.html":  tplBase,
		"index.html": tplIdx,
		"post.html":  tplPost,
	} {
		if err := os.WriteFile(filepath.Join(tplDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	contentDir := filepath.Join(dir, "content")
	if err := os.MkdirAll(filepath.Join(contentDir, "posts"), 0o755); err != nil {
		t.Fatal(err)
	}
	posts, err := post.NewStore(contentDir)
	if err != nil {
		t.Fatal(err)
	}

	wmStore, err := webmention.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wmStore.Close() })
	wmLog, err := webmention.NewLogger(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wm := webmention.New(wmStore, wmLog, "https://example.test")

	cfg := &config.Config{
		Site: config.Site{
			Title:       "Test Site",
			Author:      "Anon",
			BaseURL:     "https://example.test",
			Description: "test",
		},
		Paths: config.Paths{Templates: tplDir},
	}
	srv, err := New(cfg, posts, wm, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	srv.Routes(r)
	return srv, r, posts, wm
}

func TestSite_IndexEmpty(t *testing.T) {
	_, r, _, _ := newSite(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<title>Test Site</title>") {
		t.Errorf("body missing title: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "none") {
		t.Errorf("empty index didn't fall through to else branch: %s", w.Body.String())
	}
}

func TestSite_IndexListsPostsNewestFirst(t *testing.T) {
	_, r, posts, _ := newSite(t)
	// Store.Create prepends to s.order (post.go), so the second Create
	// appears first in Recent() regardless of Date.
	posts.Create("Older", "first", nil)
	posts.Create("Newer", "second", nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	pNewer := strings.Index(body, "Newer")
	pOlder := strings.Index(body, "Older")
	if pNewer < 0 || pOlder < 0 {
		t.Fatalf("posts missing in body: %s", body)
	}
	if pNewer > pOlder {
		t.Errorf("ordering reversed: newer=%d older=%d", pNewer, pOlder)
	}
}

func TestSite_NoteRoute(t *testing.T) {
	_, r, posts, _ := newSite(t)
	p, err := posts.Create("", "just a note", nil) // no title => note
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/notes/"+p.ID, nil))
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "just a note") {
		t.Errorf("note body missing: %s", w.Body.String())
	}
	// Webmention discovery hint is set on per-post pages.
	if got := w.Header().Get("Link"); !strings.Contains(got, `rel="webmention"`) {
		t.Errorf("Link header = %q, want webmention rel", got)
	}

	// Unknown id → 404.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/notes/does-not-exist", nil))
	if w.Code != 404 {
		t.Errorf("unknown note code=%d", w.Code)
	}
}

func TestSite_NoteRouteRejectsArticleID(t *testing.T) {
	_, r, posts, _ := newSite(t)
	p, _ := posts.Create("Has Title", "body", nil) // article, not a note
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/notes/"+p.ID, nil))
	if w.Code != 404 {
		t.Errorf("article via /notes/ code=%d, want 404", w.Code)
	}
}

func TestSite_ArticleRoute(t *testing.T) {
	_, r, posts, _ := newSite(t)
	p, _ := posts.Create("Hello World", "body **bold**", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", p.Path(), nil))
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Hello World") {
		t.Errorf("title missing: %s", w.Body.String())
	}
	// Wrong date in URL → 404.
	other := time.Now().AddDate(-1, 0, 0)
	mismatch := "/" + other.Format("2006/01/02") + "/" + p.Slug
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", mismatch, nil))
	if w.Code != 404 {
		t.Errorf("mismatched date code=%d, want 404", w.Code)
	}
}

func TestSite_RSSValid(t *testing.T) {
	_, r, posts, _ := newSite(t)
	posts.Create("Hello", "body", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/feed.xml", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/rss+xml") {
		t.Errorf("Content-Type=%q", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<rss") {
		t.Errorf("body not RSS: %s", body)
	}
	if !strings.Contains(body, "<title>Hello</title>") {
		t.Errorf("RSS missing item title: %s", body)
	}
	// Regression: managingEditor must be omitted when no author email is set
	// (commit 19109b5).
	if strings.Contains(body, "managingEditor") {
		t.Errorf("RSS contains managingEditor with no author email: %s", body)
	}
}

func TestSite_WebmentionAccepted(t *testing.T) {
	_, r, posts, _ := newSite(t)
	p, _ := posts.Create("Mentioned", "body", nil)
	target := "https://example.test" + p.Path()
	form := url.Values{"source": {"https://other.example/x"}, "target": {target}}
	req := httptest.NewRequest("POST", "/webmention", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Errorf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSite_WebmentionBadTarget(t *testing.T) {
	_, r, _, _ := newSite(t)
	form := url.Values{"source": {"https://other/x"}, "target": {"https://elsewhere/x"}}
	req := httptest.NewRequest("POST", "/webmention", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("offsite target code=%d, want 400", w.Code)
	}
}

func TestSite_WebmentionMissingFields(t *testing.T) {
	_, r, _, _ := newSite(t)
	req := httptest.NewRequest("POST", "/webmention", strings.NewReader("source=&target="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("missing fields code=%d, want 400", w.Code)
	}
}

func TestSite_TemplateOverrideUsesDisk(t *testing.T) {
	// The newSite helper already points cfg.Paths.Templates at a tempdir;
	// to confirm the override is what's being used (not the embedded FS),
	// modify one of the on-disk templates with a unique sentinel and
	// check it appears in output.
	_, r, posts, _ := newSite(t)
	posts.Create("Hi", "body", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), `data-id=`) {
		t.Errorf("on-disk template marker missing — override not active: %s", w.Body.String())
	}
}

func TestActiveTemplatesFS_FallsBackToEmbedded(t *testing.T) {
	// dir empty / nonexistent / lacks base.html → embedded wins.
	embedded := os.DirFS(t.TempDir()) // any FS works as a sentinel
	if got := activeTemplatesFS("", embedded); got != embedded {
		t.Error("empty dir didn't return embedded")
	}
	if got := activeTemplatesFS(filepath.Join(t.TempDir(), "nope"), embedded); got != embedded {
		t.Error("missing dir didn't return embedded")
	}
	emptyDir := t.TempDir() // exists but no base.html
	if got := activeTemplatesFS(emptyDir, embedded); got != embedded {
		t.Error("dir without base.html didn't return embedded")
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://example.com/x":           "example.com",
		"https://user:pw@host.test:8080/": "host.test",
		"not a url":                       "not a url",
		"":                                "",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q)=%q, want %q", in, got, want)
		}
	}
}
