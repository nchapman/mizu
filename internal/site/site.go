package site

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
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

	"github.com/nchapman/mizu/internal/config"
	"github.com/nchapman/mizu/internal/post"
	mizuserver "github.com/nchapman/mizu/internal/server"
	"github.com/nchapman/mizu/internal/webmention"
)

type Server struct {
	cfg   *config.Config
	posts *post.Store
	tpls  map[string]*template.Template
	wm    *webmention.Service
}

// New parses each page as its own template set, with base.html shared across
// all of them. Sharing one set would cause define-blocks (like "main") to
// collide between pages.
//
// embedded is the templates FS baked into the binary. cfg.Paths.Templates
// can override it with a directory on disk (theme experimentation,
// edit-without-rebuild).
func New(cfg *config.Config, posts *post.Store, wm *webmention.Service, embedded fs.FS) (*Server, error) {
	tplFS := activeTemplatesFS(cfg.Paths.Templates, embedded)
	funcs := template.FuncMap{
		"hostOf": hostOf,
	}
	tpls := map[string]*template.Template{}
	for _, name := range []string{"index.html", "post.html"} {
		t, err := template.New(name).Funcs(funcs).ParseFS(tplFS, "base.html", name)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		tpls[name] = t
	}
	return &Server{cfg: cfg, posts: posts, tpls: tpls, wm: wm}, nil
}

// activeTemplatesFS returns the on-disk override if it exists and
// contains base.html, otherwise the embedded snapshot.
func activeTemplatesFS(dir string, embedded fs.FS) fs.FS {
	if dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			diskFS := os.DirFS(dir)
			if _, err := fs.Stat(diskFS, "base.html"); err == nil {
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

type renderedPost struct {
	*post.Post
	HTML template.HTML
}

func (s *Server) render(p *post.Post) renderedPost {
	html, err := p.RenderHTML()
	if err != nil {
		log.Printf("render post %s: %v", p.ID, err)
	}
	return renderedPost{Post: p, HTML: template.HTML(html)}
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	recent := s.posts.Recent(50)
	rendered := make([]renderedPost, len(recent))
	for i, p := range recent {
		rendered[i] = s.render(p)
	}
	s.exec(w, "index.html", map[string]any{
		"Site":  s.cfg.Site,
		"Posts": rendered,
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
	// the in-body <link> emitted from base.html.
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
	s.exec(w, "post.html", map[string]any{
		"Site":     s.cfg.Site,
		"Post":     s.render(p),
		"Mentions": views,
	})
}

// hostOf is a template helper so post.html can render
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

// exec renders into a buffer first so a mid-render template error doesn't
// leave the client with a partial 200 response.
func (s *Server) exec(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tpls[name].ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("template %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}
