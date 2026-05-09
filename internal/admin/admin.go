package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nchapman/repeat/internal/auth"
	"github.com/nchapman/repeat/internal/config"
	"github.com/nchapman/repeat/internal/feeds"
	"github.com/nchapman/repeat/internal/media"
	"github.com/nchapman/repeat/internal/post"
	"github.com/nchapman/repeat/internal/webmention"
)

type Server struct {
	cfg    *config.Config
	posts  *post.Store
	feeds  *feeds.Service
	poller *feeds.Poller
	auth   *auth.Auth
	media  *media.Store
	wm     *webmention.Service
	dist   fs.FS           // built admin SPA (embedded by default)
	bgCtx  context.Context // lives for the process lifetime; used for fire-and-forget jobs
}

func New(bgCtx context.Context, cfg *config.Config, posts *post.Store, feedSvc *feeds.Service, poller *feeds.Poller, a *auth.Auth, m *media.Store, wm *webmention.Service, dist fs.FS) *Server {
	return &Server{bgCtx: bgCtx, cfg: cfg, posts: posts, feeds: feedSvc, poller: poller, auth: a, media: m, wm: wm, dist: dist}
}

func (s *Server) Routes(r chi.Router) {
	r.Route("/api", func(r chi.Router) {
		// Public endpoints — used by the SPA before login to decide
		// whether to render setup, login, or the app shell.
		r.Get("/me", s.me)
		r.Post("/setup", s.setup)
		r.Post("/login", s.login)
		r.Post("/logout", s.logout)

		// Everything else requires a valid session.
		r.Group(func(r chi.Router) {
			r.Use(s.auth.Middleware)

			r.Get("/posts", s.listPosts)
			r.Post("/posts", s.createPost)
			r.Patch("/posts/{id}", s.updatePost)
			r.Delete("/posts/{id}", s.deletePost)

			r.Get("/drafts", s.listDrafts)
			r.Post("/drafts", s.createDraft)
			r.Patch("/drafts/{id}", s.updateDraft)
			r.Delete("/drafts/{id}", s.deleteDraft)
			r.Post("/drafts/{id}/publish", s.publishDraft)

			r.Get("/subscriptions", s.listSubscriptions)
			r.Post("/subscriptions", s.addSubscription)
			r.Delete("/subscriptions", s.removeSubscription)

			r.Get("/timeline", s.timeline)
			r.Post("/items/{id}/read", s.markItemRead)
			r.Delete("/items/{id}/read", s.markItemUnread)

			r.Post("/media", s.uploadMedia)
		})
	})
	r.Get("/*", s.serveSPA)
}

// --- auth endpoints ---

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	var token string
	if c, err := r.Cookie(auth.CookieName); err == nil {
		token = c.Value
	}
	configured, authed := s.auth.Status(token)
	writeJSON(w, http.StatusOK, map[string]bool{
		"configured":    configured,
		"authenticated": authed,
	})
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	err := s.auth.SetPassword(in.Password, in.Token)
	if errors.Is(err, auth.ErrAlreadyConfigured) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if errors.Is(err, auth.ErrPasswordTooShort) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if errors.Is(err, auth.ErrBadSetupToken) {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Auto-login after setup so the user lands directly in the app.
	token, err := s.auth.CreateSession()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	auth.SetCookie(w, token)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.auth.Configured() {
		http.Error(w, "not configured", http.StatusConflict)
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if !s.auth.Verify(in.Password) {
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}
	token, err := s.auth.CreateSession()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	auth.SetCookie(w, token)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		s.auth.DestroySession(c.Value)
	}
	auth.ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

type postDTO struct {
	ID    string   `json:"id"`
	Title string   `json:"title,omitempty"`
	Date  string   `json:"date"`
	Tags  []string `json:"tags,omitempty"`
	Body  string   `json:"body"`
	HTML  string   `json:"html"`
	Path  string   `json:"path"`
}

func toDTO(p *post.Post) postDTO {
	html, err := p.RenderHTML()
	if err != nil {
		log.Printf("admin render post %s: %v", p.ID, err)
	}
	return postDTO{
		ID:    p.ID,
		Title: p.Title,
		Date:  p.Date.Format("2006-01-02T15:04:05Z07:00"),
		Tags:  p.Tags,
		Body:  p.Body,
		HTML:  html,
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
	s.queueWebmentions(p)
	writeJSON(w, http.StatusCreated, toDTO(p))
}

// queueWebmentions fires off outbound webmentions for the rendered
// post HTML. Used after both create and update — re-sending after an
// edit re-notifies receivers for current links and is spec-allowed.
// Removed-link notifications (when an edit drops a link) are a known
// gap; they require remembering the previous link set.
func (s *Server) queueWebmentions(p *post.Post) {
	go func(p *post.Post) {
		ctx, cancel := context.WithTimeout(s.bgCtx, 2*time.Minute)
		defer cancel()
		html, err := p.RenderHTML()
		if err != nil {
			log.Printf("render for webmentions: %v", err)
			return
		}
		s.wm.SendForPost(ctx, s.cfg.Site.BaseURL+p.Path(), html)
	}(p)
}

func (s *Server) updatePost(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
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
	p, err := s.posts.Update(id, in.Title, in.Body, in.Tags)
	switch {
	case errors.Is(err, post.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, post.ErrTypeToggle):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.queueWebmentions(p)
	writeJSON(w, http.StatusOK, toDTO(p))
}

// --- drafts ---

type draftDTO struct {
	ID      string   `json:"id"`
	Title   string   `json:"title,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Body    string   `json:"body"`
	HTML    string   `json:"html"`
	Created string   `json:"created"`
}

func toDraftDTO(d *post.Draft) draftDTO {
	html, err := d.RenderHTML()
	if err != nil {
		log.Printf("admin render draft %s: %v", d.ID, err)
	}
	return draftDTO{
		ID:      d.ID,
		Title:   d.Title,
		Tags:    d.Tags,
		Body:    d.Body,
		HTML:    html,
		Created: d.Created.Format(time.RFC3339),
	}
}

func (s *Server) listDrafts(w http.ResponseWriter, _ *http.Request) {
	list := s.posts.ListDrafts()
	out := make([]draftDTO, len(list))
	for i, d := range list {
		out[i] = toDraftDTO(d)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createDraft(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string   `json:"title"`
		Body  string   `json:"body"`
		Tags  []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	d, err := s.posts.CreateDraft(in.Title, in.Body, in.Tags)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toDraftDTO(d))
}

func (s *Server) updateDraft(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Title string   `json:"title"`
		Body  string   `json:"body"`
		Tags  []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	d, err := s.posts.UpdateDraft(id, in.Title, in.Body, in.Tags)
	switch {
	case errors.Is(err, post.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toDraftDTO(d))
}

func (s *Server) deleteDraft(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	err := s.posts.DeleteDraft(id)
	switch {
	case errors.Is(err, post.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) publishDraft(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := s.posts.Publish(id)
	switch {
	case errors.Is(err, post.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, post.ErrSlugTaken):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case errors.Is(err, post.ErrDraftOrphan):
		// Post is live; only the draft cleanup failed. Log the
		// orphan and report success to the user.
		log.Printf("publish: %v", err)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.queueWebmentions(p)
	writeJSON(w, http.StatusOK, toDTO(p))
}

func (s *Server) deletePost(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	err := s.posts.Delete(id)
	switch {
	case errors.Is(err, post.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	list, err := s.feeds.ListFeeds(r.Context())
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
	if errors.Is(err, feeds.ErrBlockedAddress) {
		http.Error(w, "feed url resolves to a blocked address", http.StatusBadRequest)
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

// Cursor format: "<unix_seconds>:<item_id>". Both halves are needed
// because items often share a published_at; pagination by timestamp
// alone would skip ties or repeat them across pages.
func parseTimelineCursor(s string) (feeds.TimelineCursor, bool) {
	if s == "" {
		return feeds.TimelineCursor{}, true
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return feeds.TimelineCursor{}, false
	}
	ts, err1 := strconv.ParseInt(parts[0], 10, 64)
	id, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return feeds.TimelineCursor{}, false
	}
	c := feeds.TimelineCursor{ID: id}
	if ts > 0 {
		c.PublishedAt = time.Unix(ts, 0)
	}
	return c, true
}

func formatTimelineCursor(it feeds.Item) string {
	var ts int64
	if !it.PublishedAt.IsZero() {
		ts = it.PublishedAt.Unix()
	}
	return strconv.FormatInt(ts, 10) + ":" + strconv.FormatInt(it.ID, 10)
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	cursor, ok := parseTimelineCursor(q.Get("cursor"))
	if !ok {
		http.Error(w, "bad cursor", http.StatusBadRequest)
		return
	}
	unread := q.Get("unread") == "1"

	items, err := s.feeds.Store.Timeline(r.Context(), cursor, limit, unread)
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
		out.NextCursor = formatTimelineCursor(items[len(items)-1])
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
	err = s.feeds.Store.MarkRead(r.Context(), id, read)
	if errors.Is(err, feeds.ErrItemNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- media ---

func (s *Server) uploadMedia(w http.ResponseWriter, r *http.Request) {
	// Cap the whole request body, not just one part — protects against
	// a client sending many oversized parts inside one multipart envelope.
	r.Body = http.MaxBytesReader(w, r.Body, media.MaxSize+1<<10)
	if err := r.ParseMultipartForm(media.MaxSize + 1<<10); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer f.Close()

	saved, err := s.media.Save(f)
	switch {
	case errors.Is(err, media.ErrTooLarge):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	case errors.Is(err, media.ErrUnsupportedExt), errors.Is(err, media.ErrEmpty):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"name": saved.Name,
		"url":  saved.URL,
		"size": saved.Size,
		"mime": saved.MIME,
	})
}

// serveSPA serves the built React admin out of an fs.FS. The default
// FS is embedded into the binary at build time; cfg.Paths.AdminDist
// can override it with a directory on disk (useful for development
// without rebuilding the binary, or for future theme overrides).
//
// Unknown paths render the app shell (SPA fallback). path.Clean strips
// any traversal attempts; an fs.FS rooted at admin/dist also can't
// escape upward.
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	fsys := s.activeAdminFS()
	if fsys == nil {
		// No SPA available — neither embedded nor on disk.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(placeholderHTML))
		return
	}

	rel := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/admin")), "/")
	if rel == "" {
		rel = "index.html"
	}

	if !serveFromFS(w, r, fsys, rel) {
		// Either the path didn't exist or it pointed at a directory.
		// Render the SPA shell and let client-side routing take over.
		_ = serveFromFS(w, r, fsys, "index.html")
	}
}

// activeAdminFS returns the on-disk override if cfg.Paths.AdminDist
// points to a directory containing index.html; otherwise falls back
// to the embedded snapshot. Returns nil if neither is usable, which
// triggers the placeholder.
func (s *Server) activeAdminFS() fs.FS {
	if dir := strings.TrimSpace(s.cfg.Paths.AdminDist); dir != "" {
		if info, err := os.Stat(path.Join(dir, "index.html")); err == nil && !info.IsDir() {
			return os.DirFS(dir)
		}
	}
	if s.dist == nil {
		return nil
	}
	// Embedded admin/dist may itself be empty if the binary was built
	// without first running `npm run build` — treat that as "no SPA".
	if _, err := fs.Stat(s.dist, "index.html"); err != nil {
		return nil
	}
	return s.dist
}

// serveFromFS opens rel from fsys and writes it to w. Returns false
// if the entry is missing, a directory, or otherwise unservable, so
// the caller can fall back to index.html.
func serveFromFS(w http.ResponseWriter, r *http.Request, fsys fs.FS, rel string) bool {
	f, err := fsys.Open(rel)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		// Defensive: embedded files and *os.File both implement
		// io.Seeker, but if a future fs.FS implementation doesn't,
		// fall back to a buffered copy.
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		rs = bytes.NewReader(b)
	}
	http.ServeContent(w, r, rel, info.ModTime(), rs)
	return true
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
<p>The admin app isn't available. The binary normally embeds it at build time —
this page only appears if the binary was built without first running
<code>npm run build</code>, or if <code>paths.admin_dist</code> in your config
points at a directory missing <code>index.html</code>.</p>
<p>To rebuild from source:</p>
<pre><code>make build</code></pre>
<p>For development with hot reload, run <code>npm run dev</code> in <code>admin/</code> — it proxies API calls to this server.</p>
</body></html>`
