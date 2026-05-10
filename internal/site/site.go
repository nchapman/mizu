package site

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/feeds"
	liquid "github.com/nchapman/go-liquid"

	"github.com/nchapman/mizu/internal/config"
	"github.com/nchapman/mizu/internal/post"
	mizuserver "github.com/nchapman/mizu/internal/server"
	"github.com/nchapman/mizu/internal/webmention"
)

type Server struct {
	cfg   *config.Config
	posts *post.Store
	tpls  map[string]*liquid.Template
	wm    *webmention.Service
}

// newEnvironment returns a Liquid environment scoped to this server, with
// mizu's custom filters registered. We use a dedicated environment rather
// than the package-global one so filter registration is isolated per
// Server (and per test) — important because liquid's package-global
// registry is not safe to mutate concurrently with rendering.
func newEnvironment() *liquid.Environment {
	env := liquid.NewEnvironment()
	env.RegisterFilter("host_of", func(input any, _ ...any) any {
		s, _ := input.(string)
		return hostOf(s)
	})
	// iso8601 emits an RFC 3339 timestamp with a colon-separated offset
	// ("2006-01-02T15:04:05Z07:00"). Liquid's `date` filter uses Ruby
	// strftime which has no `%:z` directive — `%z` produces the
	// no-colon form (`+0000`) that the WHATWG <time datetime> parser
	// rejects. Keep the formatting in Go so feed validators and
	// browsers see a parseable value.
	env.RegisterFilter("iso8601", func(input any, _ ...any) any {
		if t, ok := input.(time.Time); ok {
			return t.Format(time.RFC3339)
		}
		return input
	})
	return env
}

// New parses base.liquid plus each page template. Pages are rendered
// first into a string and then composed into base via the
// `content_for_layout` variable, mirroring the Shopify layout pattern.
//
// embedded is the templates FS baked into the binary. cfg.Paths.Templates
// can override it with a directory on disk (theme experimentation,
// edit-without-rebuild).
func New(cfg *config.Config, posts *post.Store, wm *webmention.Service, embedded fs.FS) (*Server, error) {
	tplFS := activeTemplatesFS(cfg.Paths.Templates, embedded)
	env := newEnvironment()
	tpls := map[string]*liquid.Template{}
	for _, name := range []string{"base.liquid", "index.liquid", "post.liquid"} {
		src, err := fs.ReadFile(tplFS, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		t, err := env.Parse(string(src))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		tpls[name] = t.WithName(name)
	}
	return &Server{cfg: cfg, posts: posts, tpls: tpls, wm: wm}, nil
}

// activeTemplatesFS returns the on-disk override if it exists and
// contains base.liquid, otherwise the embedded snapshot.
func activeTemplatesFS(dir string, embedded fs.FS) fs.FS {
	if dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			diskFS := os.DirFS(dir)
			if _, err := fs.Stat(diskFS, "base.liquid"); err == nil {
				return diskFS
			}
		}
	}
	return embedded
}

func (s *Server) Routes(r chi.Router) {
	r.Get("/", s.index)
	r.Get("/feed.xml", s.rss)
	r.Get("/robots.txt", s.robots)
	r.Get("/sitemap.xml", s.sitemap)
	r.Get("/notes/{id}", s.note)
	r.Get("/{year:[0-9]{4}}/{month:[0-9]{2}}/{day:[0-9]{2}}/{slug}", s.article)
	r.With(mizuserver.RateLimit(s.cfg.Limits.Rate.Webmention)).Post("/webmention", s.webmention)
}

// webmention is the receive endpoint. Per spec: accept form-encoded
// source/target, return 202 Accepted on success, do verification
// async. We synchronously validate the target is on this site so
// off-site noise is rejected at the door.
func (s *Server) webmention(w http.ResponseWriter, r *http.Request) {
	// Source + target are short URLs. The configured cap (a few KiB by
	// default) is well above any legitimate request and stops a hostile
	// sender from forcing us to buffer megabytes of form body.
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

// renderedPost bundles a post with its already-rendered HTML body so
// templates don't have to call RenderHTML themselves. The embedded
// *post.Post pointer is load-bearing: go-liquid's getProperty walks
// embedded structs via reflect.FieldByName for fields like Title/Date
// and via callTemplateMethod for promoted pointer-receiver methods like
// Path(). Don't change Post to a non-pointer or unembed it without
// updating the templates.
type renderedPost struct {
	*post.Post
	HTML string
}

func (s *Server) render(p *post.Post) renderedPost {
	html, err := p.RenderHTML()
	if err != nil {
		log.Printf("render post %s: %v", p.ID, err)
	}
	return renderedPost{Post: p, HTML: html}
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	recent := s.posts.Recent(50)
	rendered := make([]renderedPost, len(recent))
	for i, p := range recent {
		rendered[i] = s.render(p)
	}
	s.exec(w, "index.liquid", "", map[string]any{
		"site":  s.cfg.Site,
		"posts": rendered,
	})
}

func (s *Server) note(w http.ResponseWriter, r *http.Request) {
	p, ok := s.posts.ByID(chi.URLParam(r, "id"))
	if !ok || !p.IsNote() {
		http.NotFound(w, r)
		return
	}
	s.renderPost(w, r, p)
}

func (s *Server) article(w http.ResponseWriter, r *http.Request) {
	year, _ := strconv.Atoi(chi.URLParam(r, "year"))
	month, _ := strconv.Atoi(chi.URLParam(r, "month"))
	day, _ := strconv.Atoi(chi.URLParam(r, "day"))
	p, ok := s.posts.BySlug(year, month, day, chi.URLParam(r, "slug"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.renderPost(w, r, p)
}

type mentionView struct {
	Source     string
	VerifiedAt time.Time
}

func (s *Server) renderPost(w http.ResponseWriter, r *http.Request, p *post.Post) {
	// Advertise the webmention endpoint via Link header so senders
	// don't have to parse our HTML to discover it. Belt + braces with
	// the in-body <link> emitted from base.liquid.
	w.Header().Add("Link", `</webmention>; rel="webmention"`)

	target := s.cfg.Site.BaseURL + p.Path()
	mentions, err := s.wm.ForTarget(r.Context(), target)
	if err != nil {
		log.Printf("webmention list %s: %v", target, err)
	}
	views := make([]mentionView, len(mentions))
	for i, m := range mentions {
		views[i] = mentionView{Source: m.Source, VerifiedAt: m.VerifiedAt}
	}
	pageTitle := s.cfg.Site.Title
	if p.Title != "" {
		pageTitle = p.Title + " · " + s.cfg.Site.Title
	}
	s.exec(w, "post.liquid", pageTitle, map[string]any{
		"site":     s.cfg.Site,
		"post":     s.render(p),
		"mentions": views,
	})
}

// hostOf is a template helper so post.liquid can render
// "host.example.com" instead of the full URL when listing mentions.
// Hostname() strips userinfo and port so we don't render
// "user:pass@example.com:8080" in the public list.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Hostname()
}

func (s *Server) rss(w http.ResponseWriter, r *http.Request) {
	// Channel <managingEditor> requires an email per RSS spec; we don't
	// have one in config, so leave Author unset and let the library omit
	// the field rather than emit a malformed " (Name)" with no address.
	feed := &feeds.Feed{
		Title:       s.cfg.Site.Title,
		Link:        &feeds.Link{Href: s.cfg.Site.BaseURL},
		Description: s.cfg.Site.Description,
		Created:     time.Now(),
	}
	for _, p := range s.posts.Recent(50) {
		html, err := p.RenderHTML()
		if err != nil {
			log.Printf("rss render %s: %v", p.ID, err)
		}
		title := p.Title
		if title == "" {
			title = p.Excerpt(80)
		}
		feed.Items = append(feed.Items, &feeds.Item{
			Id:          p.ID,
			Title:       title,
			Link:        &feeds.Link{Href: s.cfg.Site.BaseURL + p.Path()},
			Description: html,
			Created:     p.Date,
		})
	}
	var buf bytes.Buffer
	if err := feed.WriteRss(&buf); err != nil {
		log.Printf("rss write: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// robots advertises the sitemap and keeps the admin SPA out of crawler
// indexes. The admin endpoints already require auth, but discouraging
// crawlers from hammering /admin/* avoids noisy 401/redirect traffic in
// search consoles.
func (s *Server) robots(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	b.WriteString("Disallow: /admin/\n")
	// Sitemap is a non-group, file-level directive; convention (and some
	// parsers) want it separated from the user-agent record by a blank line.
	if base := strings.TrimRight(s.cfg.Site.BaseURL, "/"); base != "" {
		fmt.Fprintf(&b, "\nSitemap: %s/sitemap.xml\n", base)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, b.String())
}

// sitemap lists the homepage and every published post. lastmod uses the
// post's Date — Update() doesn't currently track a separate modified
// timestamp, and crawlers tolerate a stable Date better than a missing
// field.
func (s *Server) sitemap(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(s.cfg.Site.BaseURL, "/")
	all := s.posts.Recent(0)

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")

	writeURL := func(loc string, lastmod time.Time) {
		buf.WriteString("  <url>\n    <loc>")
		_ = xml.EscapeText(&buf, []byte(loc))
		buf.WriteString("</loc>\n")
		if !lastmod.IsZero() {
			fmt.Fprintf(&buf, "    <lastmod>%s</lastmod>\n", lastmod.UTC().Format("2006-01-02"))
		}
		buf.WriteString("  </url>\n")
	}

	homeLastmod := time.Time{}
	if len(all) > 0 {
		homeLastmod = all[0].Date
	}
	writeURL(base+"/", homeLastmod)
	for _, p := range all {
		writeURL(base+p.Path(), p.Date)
	}
	buf.WriteString("</urlset>\n")

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// exec renders the named page template, then composes it into base.liquid
// via `content_for_layout`. We render to an in-memory buffer first so a
// mid-render template error doesn't leave the client with a partial 200
// response.
func (s *Server) exec(w http.ResponseWriter, name, pageTitle string, data map[string]any) {
	page, ok := s.tpls[name]
	if !ok {
		log.Printf("template %s: not found", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	body, err := page.Render(data)
	if err != nil {
		log.Printf("template %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	layoutData := map[string]any{
		"site":               s.cfg.Site,
		"page_title":         pageTitle,
		"content_for_layout": body,
	}
	var buf bytes.Buffer
	if err := s.tpls["base.liquid"].RenderTo(&buf, layoutData); err != nil {
		log.Printf("template base.liquid: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}
