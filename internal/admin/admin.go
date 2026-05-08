package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nchapman/repeat/internal/config"
	"github.com/nchapman/repeat/internal/feeds"
	"github.com/nchapman/repeat/internal/post"
)

type Server struct {
	cfg    *config.Config
	posts  *post.Store
	feeds  *feeds.Service
	poller *feeds.Poller
	bgCtx  context.Context // lives for the process lifetime; used for fire-and-forget jobs
}

func New(bgCtx context.Context, cfg *config.Config, posts *post.Store, feedSvc *feeds.Service, poller *feeds.Poller) *Server {
	return &Server{bgCtx: bgCtx, cfg: cfg, posts: posts, feeds: feedSvc, poller: poller}
}

func (s *Server) Routes(r chi.Router) {
	// TODO: auth middleware (cookie session, password from config).
	r.Route("/api", func(r chi.Router) {
		r.Get("/posts", s.listPosts)
		r.Post("/posts", s.createPost)

		r.Get("/subscriptions", s.listSubscriptions)
		r.Post("/subscriptions", s.addSubscription)
		r.Delete("/subscriptions", s.removeSubscription)

		r.Get("/timeline", s.timeline)
		r.Post("/items/{id}/read", s.markItemRead)
		r.Delete("/items/{id}/read", s.markItemUnread)
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
	if errors.Is(err, post.ErrSlugTaken) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(p))
}

// --- subscriptions ---

type subscriptionDTO struct {
	ID            int64  `json:"id"`
	URL           string `json:"url"`
	Title         string `json:"title"`
	SiteURL       string `json:"site_url,omitempty"`
	Category      string `json:"category,omitempty"`
	LastFetchedAt string `json:"last_fetched_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

func toSubDTO(f feeds.Feed) subscriptionDTO {
	d := subscriptionDTO{
		ID: f.ID, URL: f.URL, Title: f.Title, SiteURL: f.SiteURL,
		Category: f.Category, LastError: f.LastError,
	}
	if !f.LastFetchedAt.IsZero() {
		d.LastFetchedAt = f.LastFetchedAt.Format(time.RFC3339)
	}
	return d
}

func (s *Server) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	list, err := s.feeds.Store.ListFeeds(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]subscriptionDTO, len(list))
	for i, f := range list {
		out[i] = toSubDTO(f)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) addSubscription(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL      string `json:"url"`
		Title    string `json:"title"`
		SiteURL  string `json:"site_url"`
		Category string `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	f, err := s.feeds.Subscribe(r.Context(), in.URL, in.Title, in.SiteURL, in.Category)
	if errors.Is(err, feeds.ErrInvalidURL) {
		http.Error(w, "invalid feed url", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Trigger an immediate fetch in the background so the user sees items
	// without waiting for the next poll tick. Use the server-lifetime
	// context, not r.Context() — the request returns long before the fetch
	// completes.
	go func(f feeds.Feed) {
		ctx, cancel := context.WithTimeout(s.bgCtx, 30*time.Second)
		defer cancel()
		if err := s.poller.PollOne(ctx, f); err != nil {
			log.Printf("kickoff poll %s: %v", f.URL, err)
			_ = s.feeds.Store.MarkFetchError(ctx, f.ID, err.Error())
		}
	}(*f)
	writeJSON(w, http.StatusCreated, toSubDTO(*f))
}

func (s *Server) removeSubscription(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	if url == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	if err := s.feeds.Unsubscribe(r.Context(), url); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- timeline ---

type timelineItemDTO struct {
	ID          int64  `json:"id"`
	FeedID      int64  `json:"feed_id"`
	FeedTitle   string `json:"feed_title"`
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	Author      string `json:"author,omitempty"`
	Content     string `json:"content,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	Read        bool   `json:"read"`
}

type timelineResponse struct {
	Items      []timelineItemDTO `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	var before time.Time
	if c := q.Get("cursor"); c != "" {
		if t, err := time.Parse(time.RFC3339, c); err == nil {
			before = t
		}
	}
	unread := q.Get("unread") == "1"

	items, err := s.feeds.Store.Timeline(r.Context(), before, limit, unread)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := timelineResponse{Items: make([]timelineItemDTO, len(items))}
	for i, it := range items {
		d := timelineItemDTO{
			ID: it.ID, FeedID: it.FeedID, FeedTitle: it.FeedTitle,
			URL: it.URL, Title: it.Title, Author: it.Author, Content: it.Content,
			Read: it.ReadAt != nil,
		}
		if !it.PublishedAt.IsZero() {
			d.PublishedAt = it.PublishedAt.Format(time.RFC3339)
		}
		out.Items[i] = d
	}
	if len(items) == limit {
		last := items[len(items)-1]
		if !last.PublishedAt.IsZero() {
			out.NextCursor = last.PublishedAt.Format(time.RFC3339)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) markItemRead(w http.ResponseWriter, r *http.Request)   { s.setItemRead(w, r, true) }
func (s *Server) markItemUnread(w http.ResponseWriter, r *http.Request) { s.setItemRead(w, r, false) }

func (s *Server) setItemRead(w http.ResponseWriter, r *http.Request, read bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.feeds.Store.MarkRead(r.Context(), id, read); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveSPA serves the built React admin from disk. Falls back to a placeholder
// page if admin/dist doesn't exist yet (i.e. you haven't run `npm run build`).
//
// Path traversal: since we use http.ServeFile (not http.FileServer), we
// must clean the URL path ourselves and verify the resolved file stays
// inside the dist directory.
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	dist, err := filepath.Abs(s.cfg.Paths.AdminDist)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	indexPath := filepath.Join(dist, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(placeholderHTML))
		return
	}

	rel := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/admin"))
	if rel == "/" {
		http.ServeFile(w, r, indexPath)
		return
	}
	full := filepath.Join(dist, filepath.FromSlash(rel))
	if !strings.HasPrefix(full, dist+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	if info, err := os.Stat(full); err == nil && !info.IsDir() {
		http.ServeFile(w, r, full)
		return
	}
	// SPA fallback: unknown paths render the app shell.
	http.ServeFile(w, r, indexPath)
}

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
