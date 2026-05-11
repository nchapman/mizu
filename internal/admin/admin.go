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
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nchapman/mizu/internal/auth"
	"github.com/nchapman/mizu/internal/config"
	"github.com/nchapman/mizu/internal/feeds"
	"github.com/nchapman/mizu/internal/media"
	"github.com/nchapman/mizu/internal/netinfo"
	"github.com/nchapman/mizu/internal/post"
	mizuserver "github.com/nchapman/mizu/internal/server"
	"github.com/nchapman/mizu/internal/webmention"
)

// TLSController is the seam admin uses to flip TLS on after the wizard
// finishes. The real implementation lives in internal/server; admin
// only sees these calls so it doesn't have to know about CertMagic.
//
// CertMagic handles retries, exponential backoff on DNS-not-ready
// errors, and rate-limit-aware ACME retries internally — admin doesn't
// need to baby-sit the issuance loop. The flow is just: call Enable,
// poll Status().
type TLSController interface {
	Enable(ctx context.Context, domains []string, email string, staging bool) error
	Status() mizuserver.TLSStatus
}

type Server struct {
	cfg        *config.Config
	configPath string
	// cfgMu guards in-memory mutations of cfg.Site / cfg.Server.TLS
	// from the wizard handlers against concurrent reads in /me and
	// the post-create path that reads cfg.Site.BaseURL. The wizard
	// runs once during onboarding so contention is essentially nil,
	// but Go's memory model still requires the synchronization.
	cfgMu    sync.RWMutex
	posts    *post.Store
	feeds    *feeds.Service
	poller   *feeds.Poller
	auth     *auth.Service
	media    *media.Store
	wm       *webmention.Service
	dist     fs.FS // built admin SPA (embedded by default)
	publicIP *netinfo.PublicIPCache
	tls      TLSController
	bgCtx    context.Context // lives for the process lifetime; used for fire-and-forget jobs
}

func New(bgCtx context.Context, cfg *config.Config, configPath string, posts *post.Store, feedSvc *feeds.Service, poller *feeds.Poller, a *auth.Service, m *media.Store, wm *webmention.Service, ipCache *netinfo.PublicIPCache, tlsCtrl TLSController, dist fs.FS) *Server {
	return &Server{
		bgCtx: bgCtx, cfg: cfg, configPath: configPath,
		posts: posts, feeds: feedSvc, poller: poller, auth: a, media: m, wm: wm,
		publicIP: ipCache, tls: tlsCtrl, dist: dist,
	}
}

func (s *Server) Routes(r chi.Router) {
	r.Route("/api", func(r chi.Router) {
		// Public endpoints — used by the SPA before login to decide
		// whether to render setup, login, or the app shell. Rate-limited
		// per IP so an attacker can't brute-force the password or
		// the one-time setup token.
		r.Get("/me", s.me)
		r.With(mizuserver.RateLimit(s.cfg.Limits.Rate.Setup)).Post("/setup", s.setup)
		r.With(mizuserver.RateLimit(s.cfg.Limits.Rate.Setup)).Post("/setup/dns-check", s.setupDNSCheck)
		r.With(mizuserver.RateLimit(s.cfg.Limits.Rate.Login)).Post("/login", s.login)
		// Logout is reachable pre-auth (a stale cookie should still be
		// clearable) but the per-IP cap keeps it from being abused as
		// part of a denial-of-service or session-probe pattern.
		r.With(mizuserver.RateLimit(s.cfg.Limits.Rate.Login)).Post("/logout", s.logout)

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

			r.Get("/stream", s.stream)
			r.Post("/items/{id}/read", s.markItemRead)
			r.Delete("/items/{id}/read", s.markItemUnread)

			r.Post("/media", s.uploadMedia)

			r.Get("/mentions", s.listMentions)

			// Wizard endpoints that need an authenticated session: the
			// account-creation step runs unauthenticated (via /setup
			// above), then logs the operator in; from there everything
			// else flows through the cookie. Rate-limited under the
			// setup budget to keep brute-force surface minimal.
			r.With(mizuserver.RateLimit(s.cfg.Limits.Rate.Setup)).Post("/setup/site", s.setupSite)
			r.With(mizuserver.RateLimit(s.cfg.Limits.Rate.Setup)).Post("/setup/enable-tls", s.setupEnableTLS)
			r.Get("/setup/tls-status", s.setupTLSStatus)

			// User management — any authenticated user can add or
			// remove others, change their own password. No roles.
			r.Get("/users", s.listUsers)
			r.Post("/users", s.createUser)
			r.Delete("/users/{id}", s.deleteUser)
			// Password change is rate-limited because, while it requires
			// a valid session, the old_password check is a credential
			// oracle a stolen session could brute-force against bcrypt
			// at the wire-speed limit. Reusing the login budget — same
			// shape (per-IP credential probe), no need for a fresh knob.
			r.With(mizuserver.RateLimit(s.cfg.Limits.Rate.Login)).Post("/me/password", s.changeOwnPassword)
		})
	})
	r.Get("/*", s.serveSPA)
}

// --- auth endpoints ---

type userDTO struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
	LastLoginAt string `json:"last_login_at,omitempty"`
}

func toUserDTO(u *auth.User) userDTO {
	dto := userDTO{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		CreatedAt:   u.CreatedAt.Format(time.RFC3339),
	}
	if !u.LastLoginAt.IsZero() {
		dto.LastLoginAt = u.LastLoginAt.Format(time.RFC3339)
	}
	return dto
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	configured, err := s.auth.Configured(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.cfgMu.RLock()
	siteTitle := s.cfg.Site.Title
	s.cfgMu.RUnlock()
	resp := map[string]any{
		"configured":    configured,
		"authenticated": false,
		"site_title":    siteTitle,
	}
	if !configured {
		win, err := s.auth.Window(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp["setup_window"] = win
	}
	if c, err := r.Cookie(auth.CookieName); err == nil {
		if u, err := s.auth.Lookup(r.Context(), c.Value); err == nil && u != nil {
			resp["authenticated"] = true
			resp["user"] = toUserDTO(u)
			// TLS state is folded in only for authenticated callers —
			// it carries no PII but advertising "HTTPS is off" to
			// unauthenticated visitors is needless fingerprinting.
			if s.tls != nil {
				resp["tls"] = s.tls.Status()
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if !decodeJSON(w, r, s.cfg.Limits.Body.Setup, &in) {
		return
	}
	u, err := s.auth.Setup(r.Context(), in.Email, in.Password, in.DisplayName)
	if code, mapped := mapAuthError(err); mapped != nil {
		http.Error(w, mapped.Error(), code)
		return
	}
	token, err := s.auth.CreateSession(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	auth.SetCookie(w, token, s.cfg.Server.TLS.Enabled)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	configured, err := s.auth.Configured(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !configured {
		http.Error(w, "not configured", http.StatusConflict)
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, s.cfg.Limits.Body.Login, &in) {
		return
	}
	u, lockedUntil, err := s.auth.Login(r.Context(), in.Email, in.Password)
	if !lockedUntil.IsZero() {
		retry := int(time.Until(lockedUntil).Seconds())
		if retry < 1 {
			retry = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		http.Error(w, "too many failed attempts; try again later", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		// Constant message — never reveal whether the email exists.
		http.Error(w, "invalid email or password", http.StatusUnauthorized)
		return
	}
	token, err := s.auth.CreateSession(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	auth.SetCookie(w, token, s.cfg.Server.TLS.Enabled)
	w.WriteHeader(http.StatusNoContent)
}

// setupDNSCheck resolves a domain via the system resolver and reports
// whether any of its A records match this server's public IP. Returns
// hints in plain English so the wizard can surface them verbatim.
// Public (pre-auth) because the wizard runs this *before* asking the
// operator to commit account details — it's the "are we ready to set
// up" pre-flight.
func (s *Server) setupDNSCheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Domain string `json:"domain"`
	}
	if !decodeJSON(w, r, s.cfg.Limits.Body.Setup, &in) {
		return
	}
	ip := ""
	if s.publicIP != nil {
		// Tight timeout: the wizard waits on this synchronously.
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		ip, _ = s.publicIP.Get(ctx)
	}
	dnsCtx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	res := netinfo.LookupDomain(dnsCtx, strings.TrimSpace(in.Domain), ip)
	writeJSON(w, http.StatusOK, res)
}

// setupSite persists the wizard's site-basics step to config.yml and
// updates the in-memory cfg copy so the running process picks up the
// new title/base_url immediately. Authenticated — the wizard runs this
// after the account-creation step.
func (s *Server) setupSite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title       string `json:"title"`
		Author      string `json:"author"`
		BaseURL     string `json:"base_url"`
		Description string `json:"description"`
		ThemeName   string `json:"theme_name"`
	}
	if !decodeJSON(w, r, s.cfg.Limits.Body.Setup, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Author = strings.TrimSpace(in.Author)
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	in.Description = strings.TrimSpace(in.Description)
	in.ThemeName = strings.TrimSpace(in.ThemeName)
	if in.Title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}
	if in.BaseURL == "" {
		http.Error(w, "base_url required", http.StatusBadRequest)
		return
	}
	if u, err := url.Parse(in.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		http.Error(w, "base_url must be an http(s) URL with a host", http.StatusBadRequest)
		return
	}
	if err := config.WriteSite(s.configPath, config.SiteSettings{
		Title:       in.Title,
		Author:      in.Author,
		BaseURL:     in.BaseURL,
		Description: in.Description,
		ThemeName:   in.ThemeName,
	}); err != nil {
		http.Error(w, "write config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Mutate the in-memory cfg the rest of the process is reading.
	// The render pipeline reloads its own snapshot from disk via the
	// fsnotify watch, so it'll pick up the new values too — this just
	// covers the handlers that read s.cfg directly (e.g. /me's
	// site_title field, the webmention target check).
	s.cfgMu.Lock()
	s.cfg.Site.Title = in.Title
	s.cfg.Site.Author = in.Author
	s.cfg.Site.BaseURL = in.BaseURL
	s.cfg.Site.Description = in.Description
	if in.ThemeName != "" {
		s.cfg.Theme.Name = in.ThemeName
	}
	s.cfgMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// setupEnableTLS persists TLS settings, kicks off the in-process
// runner, and returns 202. The wizard polls /setup/tls-status until
// the issuance reaches "ready" or "error".
func (s *Server) setupEnableTLS(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Domains []string `json:"domains"`
		Email   string   `json:"email"`
		Staging bool     `json:"staging"`
	}
	if !decodeJSON(w, r, s.cfg.Limits.Body.Setup, &in) {
		return
	}
	if len(in.Domains) == 0 {
		http.Error(w, "at least one domain required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.Email) == "" {
		http.Error(w, "ACME contact email required", http.StatusBadRequest)
		return
	}
	if s.tls == nil {
		http.Error(w, "TLS controller not configured", http.StatusServiceUnavailable)
		return
	}
	// Enable binds the listeners synchronously, then hands the domain
	// off to CertMagic which manages issuance / retry / renewal on its
	// own. We don't write config.yml here — the OnEnabled callback
	// (wired in main.go to PersistTLSConfig) fires once CertMagic has
	// actually obtained a cert, so a failed issuance can't leave
	// enabled=true on disk and Fatalf the next restart.
	if err := s.tls.Enable(s.bgCtx, in.Domains, in.Email, in.Staging); err != nil {
		http.Error(w, "enable TLS: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// PersistTLSConfig is the OnEnabled callback for the TLS manager.
// Fires once CertMagic confirms a cert via `cert_obtained` (including
// renewals). Writes the live TLS settings to config.yml and updates
// the in-memory cfg.
func (s *Server) PersistTLSConfig(domains []string, email string, staging bool) {
	if err := config.WriteTLS(s.configPath, config.TLSSettings{
		Enabled: true, Domains: domains, Email: email, Staging: staging,
	}); err != nil {
		log.Printf("admin: persist tls config: %v", err)
		return
	}
	s.cfgMu.Lock()
	s.cfg.Server.TLS.Enabled = true
	s.cfg.Server.TLS.Domains = domains
	s.cfg.Server.TLS.Email = email
	s.cfg.Server.TLS.Staging = staging
	s.cfgMu.Unlock()
}

func (s *Server) setupTLSStatus(w http.ResponseWriter, _ *http.Request) {
	if s.tls == nil {
		writeJSON(w, http.StatusOK, mizuserver.TLSStatus{State: "off"})
		return
	}
	writeJSON(w, http.StatusOK, s.tls.Status())
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		_ = s.auth.DestroySession(r.Context(), c.Value)
	}
	auth.ClearCookie(w, s.cfg.Server.TLS.Enabled)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]userDTO, len(users))
	for i := range users {
		out[i] = toUserDTO(&users[i])
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if !decodeJSON(w, r, s.cfg.Limits.Body.Setup, &in) {
		return
	}
	u, err := s.auth.CreateUser(r.Context(), in.Email, in.Password, in.DisplayName)
	if code, mapped := mapAuthError(err); mapped != nil {
		http.Error(w, mapped.Error(), code)
		return
	}
	writeJSON(w, http.StatusCreated, toUserDTO(u))
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	// Refuse self-deletion. The lower-level DeleteUser would allow it
	// when another user exists, but that's a footgun: the operator
	// drops their own account and has to remember another user's
	// credentials to get back in. Rejecting at the handler is a small
	// guardrail, not a security boundary.
	if caller := auth.UserFrom(r.Context()); caller != nil && caller.ID == id {
		http.Error(w, "cannot delete your own account", http.StatusForbidden)
		return
	}
	err = s.auth.DeleteUser(r.Context(), id)
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, auth.ErrLastUser):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// If the deleted user was the caller, the next request will fail
	// auth because the cookie's session was cascaded away.
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	caller := auth.UserFrom(r.Context())
	if caller == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var in struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if !decodeJSON(w, r, s.cfg.Limits.Body.Login, &in) {
		return
	}
	err := s.auth.ChangePassword(r.Context(), caller.ID, in.OldPassword, in.NewPassword)
	if code, mapped := mapAuthError(err); mapped != nil {
		http.Error(w, mapped.Error(), code)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mapAuthError translates a service-layer error into (status, clientError).
// Returns (0, nil) when err is nil. Centralised so every endpoint
// surfaces the same status codes for the same auth conditions.
func mapAuthError(err error) (int, error) {
	switch {
	case err == nil:
		return 0, nil
	case errors.Is(err, auth.ErrAlreadyConfigured):
		return http.StatusConflict, err
	case errors.Is(err, auth.ErrPasswordTooShort), errors.Is(err, auth.ErrInvalidEmail):
		return http.StatusBadRequest, err
	case errors.Is(err, auth.ErrSetupWindowClosed):
		return http.StatusGone, err
	case errors.Is(err, auth.ErrEmailTaken):
		return http.StatusConflict, err
	case errors.Is(err, auth.ErrInvalidPassword):
		return http.StatusUnauthorized, err
	case errors.Is(err, auth.ErrUserNotFound):
		return http.StatusNotFound, err
	case errors.Is(err, auth.ErrLastUser):
		return http.StatusConflict, err
	case errors.Is(err, auth.ErrNotConfigured):
		return http.StatusConflict, err
	default:
		return http.StatusInternalServerError, err
	}
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

// toDTO renders the post body and packages everything the admin SPA
// needs. Render errors propagate up: a silent empty `html` field would
// just produce a blank list entry that's hard to diagnose. Callers
// 500 on error.
func toDTO(p *post.Post) (postDTO, error) {
	html, err := p.RenderHTML()
	if err != nil {
		return postDTO{}, err
	}
	return postDTO{
		ID:    p.ID,
		Title: p.Title,
		Date:  p.Date.Format("2006-01-02T15:04:05Z07:00"),
		Tags:  p.Tags,
		Body:  p.Body,
		HTML:  html,
		Path:  p.Path(),
	}, nil
}

func (s *Server) listPosts(w http.ResponseWriter, _ *http.Request) {
	recent := s.posts.Recent(100)
	out := make([]postDTO, len(recent))
	for i, p := range recent {
		dto, err := toDTO(p)
		if err != nil {
			log.Printf("admin render post %s: %v", p.ID, err)
			http.Error(w, "render failed", http.StatusInternalServerError)
			return
		}
		out[i] = dto
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string   `json:"title"`
		Body  string   `json:"body"`
		Tags  []string `json:"tags"`
	}
	if !decodeJSON(w, r, s.cfg.Limits.Body.Post, &in) {
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
	dto, err := toDTO(p)
	if err != nil {
		log.Printf("admin render post %s: %v", p.ID, err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	s.queueWebmentions(p, dto.HTML)
	writeJSON(w, http.StatusCreated, dto)
}

// queueWebmentions fires off outbound webmentions for the rendered
// post HTML. Used after both create and update — re-sending after an
// edit re-notifies receivers for current links and is spec-allowed.
// Removed-link notifications (when an edit drops a link) are a known
// gap; they require remembering the previous link set.
//
// html is passed in (rather than re-rendered here) so a single
// create/update doesn't render the body twice.
func (s *Server) queueWebmentions(p *post.Post, html string) {
	s.cfgMu.RLock()
	baseURL := s.cfg.Site.BaseURL
	s.cfgMu.RUnlock()
	target := baseURL + p.Path()
	go func() {
		ctx, cancel := context.WithTimeout(s.bgCtx, 2*time.Minute)
		defer cancel()
		s.wm.SendForPost(ctx, target, html)
	}()
}

func (s *Server) updatePost(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Title string   `json:"title"`
		Body  string   `json:"body"`
		Tags  []string `json:"tags"`
	}
	if !decodeJSON(w, r, s.cfg.Limits.Body.Post, &in) {
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
	dto, err := toDTO(p)
	if err != nil {
		log.Printf("admin render post %s: %v", p.ID, err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	s.queueWebmentions(p, dto.HTML)
	writeJSON(w, http.StatusOK, dto)
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

func toDraftDTO(d *post.Draft) (draftDTO, error) {
	html, err := d.RenderHTML()
	if err != nil {
		return draftDTO{}, err
	}
	return draftDTO{
		ID:      d.ID,
		Title:   d.Title,
		Tags:    d.Tags,
		Body:    d.Body,
		HTML:    html,
		Created: d.Created.Format(time.RFC3339),
	}, nil
}

func (s *Server) listDrafts(w http.ResponseWriter, _ *http.Request) {
	list := s.posts.ListDrafts()
	out := make([]draftDTO, len(list))
	for i, d := range list {
		dto, err := toDraftDTO(d)
		if err != nil {
			log.Printf("admin render draft %s: %v", d.ID, err)
			http.Error(w, "render failed", http.StatusInternalServerError)
			return
		}
		out[i] = dto
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createDraft(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string   `json:"title"`
		Body  string   `json:"body"`
		Tags  []string `json:"tags"`
	}
	if !decodeJSON(w, r, s.cfg.Limits.Body.Post, &in) {
		return
	}
	d, err := s.posts.CreateDraft(in.Title, in.Body, in.Tags)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dto, err := toDraftDTO(d)
	if err != nil {
		log.Printf("admin render draft %s: %v", d.ID, err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (s *Server) updateDraft(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Title string   `json:"title"`
		Body  string   `json:"body"`
		Tags  []string `json:"tags"`
	}
	if !decodeJSON(w, r, s.cfg.Limits.Body.Post, &in) {
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
	dto, err := toDraftDTO(d)
	if err != nil {
		log.Printf("admin render draft %s: %v", d.ID, err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, dto)
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
	dto, err := toDTO(p)
	if err != nil {
		log.Printf("admin render post %s: %v", p.ID, err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	s.queueWebmentions(p, dto.HTML)
	writeJSON(w, http.StatusOK, dto)
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
	// Subscriptions are URLs + a short title; reuse the login cap as a
	// generic small-payload bound rather than threading a fifth body
	// limit through config.
	if !decodeJSON(w, r, s.cfg.Limits.Body.Login, &in) {
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

// --- feed item read state ---

// timelineItemDTO is the JSON shape used by /admin/api/stream for the
// "feed" arm of its tagged-union response. The corresponding GET endpoint
// was retired once the unified stream landed.
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

type mentionDTO struct {
	ID          int64  `json:"id"`
	Source      string `json:"source"`
	SourceHost  string `json:"source_host"`
	Target      string `json:"target"`
	TargetPath  string `json:"target_path"`
	TargetTitle string `json:"target_title,omitempty"`
	ReceivedAt  string `json:"received_at"`
	VerifiedAt  string `json:"verified_at,omitempty"`
}

func (s *Server) listMentions(w http.ResponseWriter, r *http.Request) {
	ms, err := s.wm.Recent(r.Context(), 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]mentionDTO, len(ms))
	for i, m := range ms {
		dto := mentionDTO{
			ID:         m.ID,
			Source:     m.Source,
			SourceHost: hostOf(m.Source),
			Target:     m.Target,
			TargetPath: pathOf(m.Target),
			ReceivedAt: m.ReceivedAt.UTC().Format(time.RFC3339),
		}
		if p := s.lookupPostByPath(dto.TargetPath); p != nil {
			dto.TargetTitle = p.Title
		}
		if !m.VerifiedAt.IsZero() {
			dto.VerifiedAt = m.VerifiedAt.UTC().Format(time.RFC3339)
		}
		out[i] = dto
	}
	writeJSON(w, http.StatusOK, out)
}

// lookupPostByPath finds the post that matches a target URL path. The
// public-site routes are /notes/{id} (notes) and /YYYY/MM/DD/{slug}
// (titled posts); both shapes are recognised so the admin can display
// the post's title alongside an incoming mention.
func (s *Server) lookupPostByPath(p string) *post.Post {
	if p == "" {
		return nil
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) == 2 && parts[0] == "notes" {
		if pp, ok := s.posts.ByID(parts[1]); ok {
			return pp
		}
		return nil
	}
	if len(parts) == 4 {
		y, err1 := strconv.Atoi(parts[0])
		mo, err2 := strconv.Atoi(parts[1])
		d, err3 := strconv.Atoi(parts[2])
		if err1 == nil && err2 == nil && err3 == nil {
			if pp, ok := s.posts.BySlug(y, mo, d, parts[3]); ok {
				return pp
			}
		}
	}
	return nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Host
}

func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Path == "" {
		return "/"
	}
	return u.Path
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

// decodeJSON reads up to n bytes from r.Body and decodes them into
// dst. Oversize bodies become 413; malformed JSON becomes 400. The
// caller checks the bool and returns immediately on false. Centralizing
// the limit + decode keeps every endpoint consistent.
func decodeJSON(w http.ResponseWriter, r *http.Request, n int64, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, n)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "bad json", http.StatusBadRequest)
		return false
	}
	return true
}

const placeholderHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>mizu admin</title>
<style>body{font:14px/1.5 system-ui;max-width:640px;margin:4em auto;padding:0 1em;color:#222}
code{background:#f3f3f3;padding:.1em .3em;border-radius:3px}</style></head>
<body>
<h1>mizu admin</h1>
<p>The admin app isn't available. The binary normally embeds it at build time —
this page only appears if the binary was built without first running
<code>npm run build</code>, or if <code>paths.admin_dist</code> in your config
points at a directory missing <code>index.html</code>.</p>
<p>To rebuild from source:</p>
<pre><code>make build</code></pre>
<p>For development with hot reload, run <code>npm run dev</code> in <code>admin/</code> — it proxies API calls to this server.</p>
</body></html>`
