package site

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/nchapman/mizu/internal/config"
	mizudb "github.com/nchapman/mizu/internal/db"
	"github.com/nchapman/mizu/internal/webmention"
)

// newSite returns a fully wired site.Server backed by a temp PublicDir
// populated with a few baked files. The webmention service is
// constructed against an in-memory SQLite + log.
func newSite(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	publicDir := t.TempDir()
	cfg := &config.Config{
		Site: config.Site{Title: "Test", BaseURL: "https://example.test"},
	}
	cfg.ApplyDefaults()

	conn, err := mizudb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	wm := webmention.New(webmention.NewStore(conn), "https://example.test")

	srv := New(cfg, wm, publicDir, nil)
	r := chi.NewRouter()
	srv.Routes(r)
	return srv, r, publicDir
}

// writePublic seeds a file under PublicDir for FileServer-backed tests.
func writePublic(t *testing.T, publicDir, rel string, body string) {
	t.Helper()
	abs := filepath.Join(publicDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSite_ServesBakedHomepage(t *testing.T) {
	_, r, publicDir := newSite(t)
	writePublic(t, publicDir, "index.html", "<h1>baked</h1>")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "baked") {
		t.Errorf("body=%s", w.Body.String())
	}
	// HTML responses get the must-revalidate cache header and a
	// webmention Link header.
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "must-revalidate") {
		t.Errorf("Cache-Control=%q", got)
	}
	if got := w.Header().Get("Link"); !strings.Contains(got, `rel="webmention"`) {
		t.Errorf("Link=%q, want webmention rel", got)
	}
}

func TestSite_UnconfiguredServesPlaceholderHTML(t *testing.T) {
	publicDir := t.TempDir()
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	conn, _ := mizudb.Open(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { _ = conn.Close() })
	wm := webmention.New(webmention.NewStore(conn), "https://example.test")
	srv := New(cfg, wm, publicDir, func(context.Context) (bool, error) { return false, nil })
	r := chi.NewRouter()
	srv.Routes(r)

	writePublic(t, publicDir, "index.html", "<h1>real site</h1>")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	if strings.Contains(w.Body.String(), "real site") {
		t.Error("unconfigured site leaked the baked homepage")
	}
	if !strings.Contains(w.Body.String(), "awaiting setup") &&
		!strings.Contains(strings.ToLower(w.Body.String()), "being set up") {
		t.Errorf("body=%s", w.Body.String())
	}
	if got := w.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Errorf("X-Robots-Tag=%q, want noindex", got)
	}

	// feed.xml gets a 503 with Retry-After so aggregators back off.
	wf := httptest.NewRecorder()
	r.ServeHTTP(wf, httptest.NewRequest("GET", "/feed.xml", nil))
	if wf.Code != http.StatusServiceUnavailable {
		t.Errorf("feed.xml code=%d, want 503", wf.Code)
	}
	if wf.Header().Get("Retry-After") == "" {
		t.Error("feed.xml missing Retry-After")
	}
}

func TestSite_AssetWithHashGetsImmutable(t *testing.T) {
	_, r, publicDir := newSite(t)
	writePublic(t, publicDir, "assets/style.css", "body{}")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/assets/style.css?v=deadbeef", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("hashed asset Cache-Control=%q, want immutable", got)
	}
}

func TestSite_AssetWithoutHashGetsShortCache(t *testing.T) {
	_, r, publicDir := newSite(t)
	writePublic(t, publicDir, "assets/style.css", "body{}")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/assets/style.css", nil))
	if got := w.Header().Get("Cache-Control"); strings.Contains(got, "immutable") {
		t.Errorf("bare asset must not be immutable: %q", got)
	}
}

func TestSite_MissingAssetIsNot404Cached(t *testing.T) {
	// A 404 must not get the immutable header — otherwise a CDN pins the
	// absence for a year and adding the file later doesn't propagate.
	_, r, _ := newSite(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/assets/missing.css?v=deadbeef", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("code=%d, want 404", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); strings.Contains(got, "immutable") {
		t.Errorf("404 should not be immutable: %q", got)
	}
}

func TestSite_DraftsGetNoindex(t *testing.T) {
	_, r, publicDir := newSite(t)
	writePublic(t, publicDir, "_drafts/abc123/index.html", "<p>draft</p>")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/_drafts/abc123/", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Errorf("X-Robots-Tag=%q, want noindex", got)
	}
}

func TestSite_WebmentionAccepted(t *testing.T) {
	_, r, _ := newSite(t)
	form := url.Values{"source": {"https://other/x"}, "target": {"https://example.test/2026/05/10/foo"}}
	req := httptest.NewRequest("POST", "/webmention", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Errorf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSite_WebmentionBadTarget(t *testing.T) {
	_, r, _ := newSite(t)
	form := url.Values{"source": {"https://other/x"}, "target": {"https://elsewhere/x"}}
	req := httptest.NewRequest("POST", "/webmention", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("offsite target code=%d, want 400", w.Code)
	}
}

func TestSite_WebmentionOversizeBodyRejected(t *testing.T) {
	srv, r, _ := newSite(t)
	pad := strings.Repeat("a", int(srv.cfg.Limits.Body.Webmention)+1)
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
	srv, r, _ := newSite(t)
	form := url.Values{"source": {"https://other/x"}, "target": {"https://example.test/x"}}.Encode()
	limit := srv.cfg.Limits.Rate.Webmention.Requests
	for i := 0; i < limit; i++ {
		req := httptest.NewRequest("POST", "/webmention", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("hit 429 too early at %d", i)
		}
	}
	req := httptest.NewRequest("POST", "/webmention", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("code=%d, want 429", w.Code)
	}
}

func TestIsHTMLPath(t *testing.T) {
	cases := map[string]bool{
		"":                         true,
		"/":                        true,
		"/2026/05/10/foo/":         true,
		"/index.html":              true,
		"/feed.xml":                false,
		"/assets/style.css":        false,
		"/assets/style.css?v=abcd": false,
		"/assets/fonts/x.woff2":    false,
		"/sitemap.xml":             false,
		"/robots.txt":              false,
		"/notes/abc":               true,
		"/_drafts/saltedslug/":     true,
	}
	for p, want := range cases {
		// Strip the query for the helper — it inspects path only.
		path := p
		if i := strings.Index(p, "?"); i >= 0 {
			path = p[:i]
		}
		if got := isHTMLPath(path); got != want {
			t.Errorf("isHTMLPath(%q)=%v, want %v", p, got, want)
		}
	}
}
