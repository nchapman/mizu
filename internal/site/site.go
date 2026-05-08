package site

import (
	"bytes"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/feeds"

	"github.com/nchapman/repeat/internal/config"
	"github.com/nchapman/repeat/internal/post"
)

type Server struct {
	cfg   *config.Config
	posts *post.Store
	tpls  map[string]*template.Template
}

// New parses each page as its own template set, with base.html shared across
// all of them. Sharing one set would cause define-blocks (like "main") to
// collide between pages.
func New(cfg *config.Config, posts *post.Store) (*Server, error) {
	base := filepath.Join(cfg.Paths.Templates, "base.html")
	tpls := map[string]*template.Template{}
	for _, name := range []string{"index.html", "post.html"} {
		t, err := template.ParseFiles(base, filepath.Join(cfg.Paths.Templates, name))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		tpls[name] = t
	}
	return &Server{cfg: cfg, posts: posts, tpls: tpls}, nil
}

func (s *Server) Routes(r chi.Router) {
	r.Get("/", s.index)
	r.Get("/feed.xml", s.rss)
	r.Get("/notes/{id}", s.note)
	r.Get("/{year:[0-9]{4}}/{month:[0-9]{2}}/{day:[0-9]{2}}/{slug}", s.article)
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
	s.renderPost(w, p)
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
	s.renderPost(w, p)
}

func (s *Server) renderPost(w http.ResponseWriter, p *post.Post) {
	s.exec(w, "post.html", map[string]any{
		"Site": s.cfg.Site,
		"Post": s.render(p),
	})
}

func (s *Server) rss(w http.ResponseWriter, r *http.Request) {
	feed := &feeds.Feed{
		Title:       s.cfg.Site.Title,
		Link:        &feeds.Link{Href: s.cfg.Site.BaseURL},
		Description: s.cfg.Site.Description,
		Author:      &feeds.Author{Name: s.cfg.Site.Author},
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
