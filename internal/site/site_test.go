package site

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nchapman/mizu/internal/config"
	"github.com/nchapman/mizu/internal/post"
	"github.com/nchapman/mizu/internal/theme"
	"github.com/nchapman/mizu/internal/webmention"
)

// minimal templates used by every site test. They mirror the layout
// pattern used by the real templates without depending on the shipped
// default theme: pages render their content first and base.liquid
// composes it in via `content_for_layout`.
const (
	tplBase = `<!doctype html><html><head><title>{{ page_title | default: site.Title | escape }}</title><style>:root{--accent:{{ theme.settings.accent_color | css_value }};}</style></head><body><link rel="stylesheet" href="/assets/style.css?v={{ theme.version }}">{{ content_for_layout }}</body></html>`
	tplIdx  = `{% for p in posts %}<article data-id="{{ p.ID }}">{% if p.Title %}<h2><a href="{{ p.Path }}">{{ p.Title }}</a></h2>{% else %}<a href="{{ p.Path }}">note</a>{% endif %}<div class="body">{{ p.HTML }}</div></article>{% else %}<p>none</p>{% endfor %}{% if pagination.total_pages > 1 %}<nav data-page="{{ pagination.page }}" data-total="{{ pagination.total_pages }}">{% if pagination.prev_url %}<a rel="prev" href="{{ pagination.prev_url }}">prev</a>{% endif %}{% if pagination.next_url %}<a rel="next" href="{{ pagination.next_url }}">next</a>{% endif %}</nav>{% endif %}`
	tplPost = `<article>{% if post.Title %}<h2>{{ post.Title }}</h2>{% endif %}<div class="body">{{ post.HTML }}</div></article>{% if mentions %}<ul>{% for m in mentions %}<li>{{ m.Source | host_of }}</li>{% endfor %}</ul>{% endif %}`
)

// fakeThemeFS returns an embedded-shape FS containing a single
// "default" theme with the test templates above. It mirrors what
// theme.Load expects to receive from themesFS() in production.
func fakeThemeFS(extra map[string]string) fstest.MapFS {
	files := fstest.MapFS{
		"default/theme.yaml": &fstest.MapFile{Data: []byte(`name: Test Default
version: "1"
settings:
  accent_color: "#0066cc"
  max_width: "640px"
`)},
		"default/base.liquid":      &fstest.MapFile{Data: []byte(tplBase)},
		"default/index.liquid":     &fstest.MapFile{Data: []byte(tplIdx)},
		"default/post.liquid":      &fstest.MapFile{Data: []byte(tplPost)},
		"default/assets/style.css": &fstest.MapFile{Data: []byte("body{color:#222}")},
	}
	for name, body := range extra {
		files["default/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return files
}

// newSite builds a fully wired site Server backed by an in-memory
// theme, posts on tempdir, and a webmention service. The returned
// router has Routes registered.
func newSite(t *testing.T) (*Server, http.Handler, *post.Store, *webmention.Service) {
	t.Helper()
	return newSiteWithTheme(t, nil, nil)
}

// newSiteWithTheme is newSite plus optional theme overrides and extra
// theme files (e.g. partials, assets). Used by tests that need to
// drive css_value, asset serving, or theme.settings overrides.
func newSiteWithTheme(t *testing.T, overrides map[string]any, extraFiles map[string]string) (*Server, http.Handler, *post.Store, *webmention.Service) {
	t.Helper()
	dir := t.TempDir()

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
	}
	cfg.ApplyDefaults()

	// theme.Load checks ./themes/<name> on disk first; chdir to a clean
	// dir so the test can't accidentally pick up the repo's themes/.
	t.Chdir(t.TempDir())
	th, err := theme.Load("default", fakeThemeFS(extraFiles), overrides)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, posts, wm, th)
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
	// A title-less post falls back to the site title alone — no
	// "Title · Site" composition.
	if !strings.Contains(w.Body.String(), "<title>Test Site</title>") {
		t.Errorf("note title should be site title only: %s", w.Body.String())
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
	// Article pages compose <title> as "Post Title · Site Title".
	if !strings.Contains(w.Body.String(), "<title>Hello World · Test Site</title>") {
		t.Errorf("article title composition wrong: %s", w.Body.String())
	}
	// Sanitized markdown HTML must reach the response unescaped — the
	// trust boundary is at ingest, not at render. Catches an
	// accidentally-added `| escape` on the post body.
	if !strings.Contains(w.Body.String(), "<strong>bold</strong>") {
		t.Errorf("rendered markdown HTML was escaped or missing: %s", w.Body.String())
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

// seedPosts creates n posts; first call is "Post-1" (oldest), last is
// "Post-N" (newest, since Recent reverses creation order). Returns the
// titles as they appear in newest-first order so tests can assert
// page slices.
func seedPosts(t *testing.T, posts *post.Store, n int) []string {
	t.Helper()
	titles := make([]string, n)
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("Post-%d", i+1)
		if _, err := posts.Create(title, "body", nil); err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		titles[n-1-i] = title // newest-first ordering matches Recent/Page
	}
	return titles
}

func TestSite_PaginationFirstPageNoNav(t *testing.T) {
	// With fewer than postsPerPage posts there is no second page, so
	// the nav block should not render at all — keeps the homepage clean
	// for new sites.
	_, r, posts, _ := newSite(t)
	posts.Create("only", "body", nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if strings.Contains(w.Body.String(), "data-page=") {
		t.Errorf("nav rendered with only one page: %s", w.Body.String())
	}
}

func TestSite_PaginationListsCorrectSlice(t *testing.T) {
	// Page 2 should contain the next postsPerPage posts in newest-first
	// order. 25 posts → page 1 has Post-25..Post-6, page 2 has Post-5..Post-1.
	_, r, posts, _ := newSite(t)
	titles := seedPosts(t, posts, 25)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/?page=2", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range titles[20:] {
		if !strings.Contains(body, want) {
			t.Errorf("page 2 missing %s", want)
		}
	}
	for _, gone := range titles[:20] {
		if strings.Contains(body, gone) {
			t.Errorf("page 2 leaked page-1 post %s", gone)
		}
	}
	// Nav points back to /, not /?page=1 (canonical), and has no next.
	if !strings.Contains(body, `rel="prev" href="/"`) {
		t.Errorf("page 2 should link prev to /: %s", body)
	}
	if strings.Contains(body, `rel="next"`) {
		t.Errorf("page 2 should not have a next link with 25 posts: %s", body)
	}
	// Title gets the page suffix on pages > 1 so search results don't
	// look like duplicate homepages.
	if !strings.Contains(body, "<title>Page 2 · Test Site</title>") {
		t.Errorf("paginated title missing: %s", body)
	}
}

func TestSite_PaginationMidRangeHasBothLinks(t *testing.T) {
	_, r, posts, _ := newSite(t)
	seedPosts(t, posts, 50) // 3 pages at 20/page

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/?page=2", nil))
	body := w.Body.String()
	if !strings.Contains(body, `rel="prev"`) || !strings.Contains(body, `rel="next" href="/?page=3"`) {
		t.Errorf("middle page missing prev+next: %s", body)
	}
}

func TestSite_PaginationOutOfRange404(t *testing.T) {
	_, r, posts, _ := newSite(t)
	seedPosts(t, posts, 5)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/?page=99", nil))
	if w.Code != 404 {
		t.Errorf("code=%d, want 404 for page past end", w.Code)
	}
}

func TestSite_PaginationExactBoundary404(t *testing.T) {
	// Exactly postsPerPage posts → only one page exists. Asking for
	// page 2 must 404 rather than 200-with-empty-list.
	_, r, posts, _ := newSite(t)
	seedPosts(t, posts, postsPerPage)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/?page=2", nil))
	if w.Code != 404 {
		t.Errorf("code=%d, want 404 at exact-boundary page", w.Code)
	}
}

func TestSite_PaginationCanonicalRedirect(t *testing.T) {
	// /?page=1 is the same content as /; redirect so caches and search
	// engines collapse them. Empty store still redirects — page is a
	// URL property, not a content property.
	_, r, _, _ := newSite(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/?page=1", nil))
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("code=%d, want 301", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("Location=%q, want /", loc)
	}
}

func TestSite_PaginationMalformedPage404(t *testing.T) {
	_, r, _, _ := newSite(t)
	for _, bad := range []string{"abc", "-1", "0", "01", "1.5"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/?page="+bad, nil))
		if w.Code != 404 {
			t.Errorf("page=%q code=%d, want 404", bad, w.Code)
		}
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

func TestSite_WebmentionOversizeBodyRejected(t *testing.T) {
	srv, r, _, _ := newSite(t)
	cap := srv.cfg.Limits.Body.Webmention
	pad := strings.Repeat("a", int(cap)+1)
	form := url.Values{"source": {"https://other/x"}, "target": {"https://example.test/x"}, "pad": {pad}}
	req := httptest.NewRequest("POST", "/webmention", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("code=%d, want 413", w.Code)
	}
}

func TestSite_WebmentionRateLimit(t *testing.T) {
	srv, r, posts, _ := newSite(t)
	p, _ := posts.Create("M", "body", nil)
	target := "https://example.test" + p.Path()
	form := url.Values{"source": {"https://other/x"}, "target": {target}}.Encode()
	limit := srv.cfg.Limits.Rate.Webmention.Requests
	for i := 0; i < limit; i++ {
		req := httptest.NewRequest("POST", "/webmention", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("hit 429 too early at request %d", i)
		}
	}
	req := httptest.NewRequest("POST", "/webmention", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("code=%d, want 429 after %d", w.Code, limit)
	}
}

func TestSite_AssetsServeFromTheme(t *testing.T) {
	_, r, _, _ := newSite(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/assets/style.css", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("content-type=%q, want text/css", ct)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff header = %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("cache-control = %q", got)
	}
	if !strings.Contains(w.Body.String(), "color:#222") {
		t.Errorf("body missing asset content: %s", w.Body.String())
	}
}

func TestSite_AssetsMissingFile404(t *testing.T) {
	_, r, _, _ := newSite(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/assets/nope.css", nil))
	if w.Code != 404 {
		t.Errorf("code=%d, want 404", w.Code)
	}
}

func TestSite_ThemeSettingsReachTemplates(t *testing.T) {
	// Operator override should win and surface as a CSS custom property
	// in the layout. Confirms the override → theme.Settings → template
	// pipeline end-to-end.
	_, r, _, _ := newSiteWithTheme(t, map[string]any{"accent_color": "#ff0066"}, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "--accent:#ff0066") {
		t.Errorf("override not reflected in :root block: %s", w.Body.String())
	}
}

func TestSite_CSSValueRejectsHostileInput(t *testing.T) {
	// A theme setting that tries to break out of the CSS declaration
	// must drop to "" so the rule cascades, not embed verbatim.
	hostile := "red; background: url(http://evil/)"
	_, r, _, _ := newSiteWithTheme(t, map[string]any{"accent_color": hostile}, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if strings.Contains(body, "evil") {
		t.Errorf("hostile CSS payload reached output: %s", body)
	}
	if !strings.Contains(body, "--accent:;") {
		t.Errorf("expected empty --accent value, got: %s", body)
	}
}

func TestSite_StylesheetLinkCachebust(t *testing.T) {
	// The default fakeThemeFS sets version: "1"; the link tag should
	// carry ?v=1 so bumping theme.yaml's version invalidates browser
	// caches without us inventing per-file fingerprinting.
	_, r, _, _ := newSite(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), `/assets/style.css?v=1`) {
		t.Errorf("link tag missing version cachebust: %s", w.Body.String())
	}
}

func TestCSSValue(t *testing.T) {
	cases := map[string]string{
		"#0066cc":             "#0066cc",
		"#abc":                "#abc",
		"  42rem  ":           "42rem", // TrimSpace runs first
		"42rem":               "42rem",
		"100%":                "100%",
		"0.5em":               "0.5em",
		"0":                   "0",
		"1.25":                "1.25",
		"transparent":         "transparent",
		"rgb(255, 0, 102)":    "rgb(255, 0, 102)",
		"":                    "",
		"red":                 "", // not in our keyword allowlist
		"red; background: x":  "",
		"javascript:alert(1)": "",
		"}":                   "",
		"#0066cc; color: red": "",
	}
	for in, want := range cases {
		if got := cssValue(in); got != want {
			t.Errorf("cssValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSite_AssetsRejectsDirectoryListing(t *testing.T) {
	// Bare /assets/ would otherwise let http.FileServer render a
	// directory listing, leaking what files the theme ships. Also pin
	// the trailing-slash-less form for completeness — chi's wildcard
	// matches both.
	_, r, _, _ := newSite(t)
	for _, path := range []string{"/assets/", "/assets"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code == 200 && strings.Contains(w.Body.String(), "style.css") {
			t.Errorf("%s leaked directory listing: %s", path, w.Body.String())
		}
	}
}

func TestSite_RobotsTxt(t *testing.T) {
	_, r, _, _ := newSite(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/robots.txt", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type=%q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "User-agent: *") {
		t.Errorf("missing user-agent line: %s", body)
	}
	if !strings.Contains(body, "Disallow: /admin/") {
		t.Errorf("admin not disallowed: %s", body)
	}
	if !strings.Contains(body, "Sitemap: https://example.test/sitemap.xml") {
		t.Errorf("sitemap link missing: %s", body)
	}
}

func TestSite_RobotsTxt_TrailingSlashBaseURL(t *testing.T) {
	// A trailing slash on BaseURL must not produce "//sitemap.xml".
	srv, router, _, _ := newSite(t)
	srv.cfg.Site.BaseURL = "https://example.test/"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/robots.txt", nil))
	if !strings.Contains(w.Body.String(), "Sitemap: https://example.test/sitemap.xml") {
		t.Errorf("trailing slash collapsed wrong: %s", w.Body.String())
	}
}

func TestSite_Sitemap(t *testing.T) {
	_, r, posts, _ := newSite(t)
	posts.Create("First", "body one", nil)
	posts.Create("", "a note", nil) // /notes/<id>
	posts.Create("Second", "body two", nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/sitemap.xml", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("content-type=%q", ct)
	}

	type urlEntry struct {
		Loc     string `xml:"loc"`
		Lastmod string `xml:"lastmod"`
	}
	var doc struct {
		XMLName xml.Name   `xml:"urlset"`
		URLs    []urlEntry `xml:"url"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("parse sitemap: %v\nbody: %s", err, w.Body.String())
	}
	if len(doc.URLs) != 4 {
		t.Fatalf("want 4 URLs (home + 3 posts), got %d: %+v", len(doc.URLs), doc.URLs)
	}
	if doc.URLs[0].Loc != "https://example.test/" {
		t.Errorf("first entry should be home, got %q", doc.URLs[0].Loc)
	}
	for _, u := range doc.URLs[1:] {
		if !strings.HasPrefix(u.Loc, "https://example.test/") {
			t.Errorf("entry %q missing base prefix", u.Loc)
		}
		if u.Lastmod == "" {
			t.Errorf("entry %q missing lastmod", u.Loc)
		}
		if _, err := time.Parse("2006-01-02", u.Lastmod); err != nil {
			t.Errorf("entry %q lastmod %q not W3C date: %v", u.Loc, u.Lastmod, err)
		}
	}
}

func TestSite_SitemapEmpty(t *testing.T) {
	// No posts: still emit a valid <urlset> with the homepage.
	_, r, _, _ := newSite(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/sitemap.xml", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<urlset") || !strings.Contains(body, "</urlset>") {
		t.Errorf("malformed empty sitemap: %s", body)
	}
	if !strings.Contains(body, "<loc>https://example.test/</loc>") {
		t.Errorf("home entry missing: %s", body)
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
