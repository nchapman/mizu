// Package site serves the public side of mizu. The pipeline (in
// internal/render) bakes every page, feed, sitemap, and asset to disk
// under cfg.Paths.Public; this package mounts a chi sub-router that
// http.FileServer's that directory plus the dynamic webmention receive
// endpoint.
//
// There is no template execution, markdown rendering, or DB query on
// the request path here — that all happens in the render pipeline,
// which runs whenever a source file changes. Steady-state requests are
// one syscall away from the kernel page cache.
package site

import (
	"context"
	_ "embed"
	"errors"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nchapman/mizu/internal/config"
	mizuserver "github.com/nchapman/mizu/internal/server"
	"github.com/nchapman/mizu/internal/webmention"
)

// ConfiguredFn reports whether the install has at least one user. Site
// uses this to flip every public route to a friendly placeholder until
// the operator finishes onboarding. nil means "always configured" — a
// convenient default for tests that don't care about the placeholder
// path.
type ConfiguredFn func(ctx context.Context) (bool, error)

// Server wires the public mux. PublicDir is the baked-output root
// produced by internal/render. The webmention service stays dynamic —
// it accepts POST /webmention, queues for verification, and the
// verifier worker enqueues a render when a mention promotes.
type Server struct {
	cfg        *config.Config
	wm         *webmention.Service
	publicDir  string
	configured ConfiguredFn

	// cachedConfigured caches the configured-state across requests so
	// the public root doesn't hit the DB on every hit during the
	// pre-setup window. Flips one-way (false → true) when the wizard
	// completes; never goes back during the process lifetime, so a
	// stale "true" can't appear post-deletion in this process. A stale
	// "false" right after setup is bounded by cachedConfiguredTTL.
	cachedConfigured atomic.Bool
	cachedAt         atomic.Int64
}

const cachedConfiguredTTL = time.Second

//go:embed placeholder.html
var placeholderHTML []byte

func New(cfg *config.Config, wm *webmention.Service, publicDir string, configured ConfiguredFn) *Server {
	return &Server{cfg: cfg, wm: wm, publicDir: publicDir, configured: configured}
}

// isConfigured returns the cached configured state, refreshing from the
// callback if the cache is stale. Errors from the callback are treated
// as "configured" — failing closed (placeholder) on a transient DB
// error would hide the real site, which is worse than the false
// negative of skipping the placeholder for one request.
func (s *Server) isConfigured(ctx context.Context) bool {
	if s.configured == nil {
		return true
	}
	if s.cachedConfigured.Load() {
		return true
	}
	now := time.Now().Unix()
	if last := s.cachedAt.Load(); now-last < int64(cachedConfiguredTTL.Seconds()) {
		return false
	}
	ok, err := s.configured(ctx)
	if err != nil {
		return true
	}
	s.cachedConfigured.Store(ok)
	s.cachedAt.Store(now)
	return ok
}

// Routes mounts:
//   - POST /webmention                   (rate-limited; receives mentions)
//   - GET/HEAD everything else from PublicDir via http.FileServer with
//     cache-control + X-Robots-Tag + Link header decoration.
func (s *Server) Routes(r chi.Router) {
	r.With(mizuserver.RateLimit(s.cfg.Limits.Rate.Webmention)).Post("/webmention", s.webmention)
	r.Handle("/*", s.gatedPublicHandler())
}

// gatedPublicHandler intercepts every public GET while the install is
// unconfigured and returns a small placeholder page (or a 503 for
// feed/sitemap requests) instead of serving the half-baked public dir.
// Once setup completes the wrapper falls through to the real handler.
func (s *Server) gatedPublicHandler() http.Handler {
	real := s.publicHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isConfigured(r.Context()) {
			real.ServeHTTP(w, r)
			return
		}
		s.servePlaceholder(w, r)
	})
}

// servePlaceholder renders the "awaiting setup" page for HTML requests
// and a 503 for machine-readable feed/sitemap paths. Feed readers and
// search crawlers should back off rather than indexing the placeholder
// as a real homepage.
func (s *Server) servePlaceholder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "no-store")
	cleaned := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	if cleaned == "/feed.xml" || cleaned == "/sitemap.xml" || cleaned == "/robots.txt" {
		w.Header().Set("Retry-After", "300")
		http.Error(w, "this mizu instance has not finished setup yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(placeholderHTML)
}

// publicHandler wraps http.FileServer with the response-header policy
// the static site needs:
//   - long-immutable Cache-Control on /assets/* requests carrying ?v=,
//     short max-age otherwise (mirrors the request-time hashing model).
//   - short max-age + must-revalidate on HTML, feed.xml, sitemap.xml,
//     robots.txt — readers should pick up post edits within ~5 min.
//   - X-Robots-Tag noindex on /_drafts/* so a leaked salted URL still
//     can't be indexed.
//   - Link rel="webmention" on HTML responses for senders who don't
//     parse the page body.
//   - X-Content-Type-Options nosniff on every response, so a stale or
//     hand-placed file can't be sniffed into something executable.
//
// Cache headers are applied lazily via cacheableResponseWriter so 404
// responses don't get immutable directives that pin the absence into
// CDN caches forever.
func (s *Server) publicHandler() http.Handler {
	fileServer := http.FileServer(http.Dir(s.publicDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Normalize before the prefix check: path.Clean collapses
		// any "%2F", "./", "..", or redundant slashes that http.FileServer
		// would also clean before resolving. Without this a request like
		// "/%2F_drafts/foo/" would slip past the noindex stamp and
		// still resolve to the draft via FileServer's own cleanup.
		cleaned := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
		if strings.HasPrefix(cleaned, "/_drafts/") {
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		}
		if isHTMLPath(cleaned) {
			w.Header().Add("Link", `</webmention>; rel="webmention"`)
		}
		crw := newCacheableResponseWriter(w, r)
		fileServer.ServeHTTP(crw, r)
	})
}

// isHTMLPath returns true for paths that resolve to an HTML document:
// directory paths (FileServer maps these to <dir>/index.html) and
// explicit .html paths. Asset, feed, and sitemap paths return false so
// they don't get a webmention Link header.
func isHTMLPath(p string) bool {
	if p == "" || strings.HasSuffix(p, "/") {
		return true
	}
	ext := filepath.Ext(p)
	return ext == "" || ext == ".html"
}

// webmention is the receive endpoint. Per spec: accept form-encoded
// source/target, return 202 Accepted on success, do verification async.
func (s *Server) webmention(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.Limits.Body.Webmention)
	if err := r.ParseForm(); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	source := r.PostForm.Get("source")
	target := r.PostForm.Get("target")
	err := s.wm.Receive(r.Context(), source, target)
	switch {
	case errors.Is(err, webmention.ErrBadTarget):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, webmention.ErrSameSource):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, webmention.ErrQueueFull):
		w.Header().Set("Retry-After", "300")
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// cacheableResponseWriter applies Cache-Control on the first
// WriteHeader call and only when the status is 200, so 404s and other
// errors don't pin themselves into CDN caches with the immutable
// directive.
type cacheableResponseWriter struct {
	http.ResponseWriter
	r       *http.Request
	written bool
}

func newCacheableResponseWriter(w http.ResponseWriter, r *http.Request) *cacheableResponseWriter {
	return &cacheableResponseWriter{ResponseWriter: w, r: r}
}

func (c *cacheableResponseWriter) WriteHeader(status int) {
	if !c.written {
		c.written = true
		if status == http.StatusOK {
			setCacheControl(c.ResponseWriter, c.r)
		}
	}
	c.ResponseWriter.WriteHeader(status)
}

func (c *cacheableResponseWriter) Write(p []byte) (int, error) {
	if !c.written {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(p)
}

// Unwrap exposes the underlying ResponseWriter so net/http's
// ResponseController and middleware like chi's compress can discover
// optional interfaces (http.Flusher, http.Hijacker) on the wrapped
// writer. Without this chained middleware would lose access to those
// capabilities through our wrapper.
func (c *cacheableResponseWriter) Unwrap() http.ResponseWriter {
	return c.ResponseWriter
}

// setCacheControl picks the right policy for a successful response.
// Hashed asset URLs (?v=…) are pinned for a year — the URL changes
// whenever the bytes do, so it's safe. Everything else is short and
// must-revalidate so post edits propagate within minutes.
func setCacheControl(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasPrefix(path, "/assets/") {
		if r.URL.Query().Has("v") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
}
