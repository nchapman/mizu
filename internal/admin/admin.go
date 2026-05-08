package admin

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/nchapman/repeat/internal/config"
	"github.com/nchapman/repeat/internal/post"
)

type Server struct {
	cfg   *config.Config
	posts *post.Store
}

func New(cfg *config.Config, posts *post.Store) *Server {
	return &Server{cfg: cfg, posts: posts}
}

func (s *Server) Routes(r chi.Router) {
	// TODO: auth middleware (cookie session, password from config).
	r.Route("/api", func(r chi.Router) {
		r.Get("/posts", s.listPosts)
		r.Post("/posts", s.createPost)
	})
	r.Get("/*", s.serveSPA)
}

type postDTO struct {
	ID    string   `json:"id"`
	Title string   `json:"title,omitempty"`
	Date  string   `json:"date"`
	Tags  []string `json:"tags,omitempty"`
	Body  string   `json:"body"`
	Path  string   `json:"path"`
}

func toDTO(p *post.Post) postDTO {
	return postDTO{
		ID:    p.ID,
		Title: p.Title,
		Date:  p.Date.Format("2006-01-02T15:04:05Z07:00"),
		Tags:  p.Tags,
		Body:  p.Body,
		Path:  p.Path(),
	}
}

func (s *Server) listPosts(w http.ResponseWriter, r *http.Request) {
	recent := s.posts.Recent(100)
	out := make([]postDTO, len(recent))
	for i, p := range recent {
		out[i] = toDTO(p)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string   `json:"title"`
		Body  string   `json:"body"`
		Tags  []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.Body) == "" {
		http.Error(w, "body required", http.StatusBadRequest)
		return
	}
	p, err := s.posts.Create(in.Title, in.Body, in.Tags)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(p))
}

// serveSPA serves the built React admin from disk. Falls back to a placeholder
// page if admin/dist doesn't exist yet (i.e. you haven't run `npm run build`).
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	dist := s.cfg.Paths.AdminDist
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(placeholderHTML))
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/admin")
	if rel == "" || rel == "/" {
		http.ServeFile(w, r, filepath.Join(dist, "index.html"))
		return
	}
	full := filepath.Join(dist, rel)
	if info, err := os.Stat(full); err == nil && !info.IsDir() {
		http.ServeFile(w, r, full)
		return
	}
	// SPA fallback: unknown paths render the app shell.
	http.ServeFile(w, r, filepath.Join(dist, "index.html"))
}

// _ keeps the fs import satisfied for future embed.FS swap.
var _ = fs.ValidPath

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const placeholderHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>repeat admin</title>
<style>body{font:14px/1.5 system-ui;max-width:640px;margin:4em auto;padding:0 1em;color:#222}
code{background:#f3f3f3;padding:.1em .3em;border-radius:3px}</style></head>
<body>
<h1>repeat admin</h1>
<p>The React admin app hasn't been built yet. From the project root:</p>
<pre><code>cd admin
npm install
npm run build</code></pre>
<p>For development with hot reload, run <code>npm run dev</code> in <code>admin/</code> — it proxies API calls to this server.</p>
<p>Meanwhile, you can post via the API:</p>
<pre><code>curl -X POST http://localhost:8080/admin/api/posts \
  -H 'content-type: application/json' \
  -d '{"body":"hello world"}'</code></pre>
</body></html>`
