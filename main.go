package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/nchapman/mizu/internal/admin"
	"github.com/nchapman/mizu/internal/auth"
	"github.com/nchapman/mizu/internal/config"
	mizudb "github.com/nchapman/mizu/internal/db"
	"github.com/nchapman/mizu/internal/feeds"
	"github.com/nchapman/mizu/internal/media"
	"github.com/nchapman/mizu/internal/netinfo"
	"github.com/nchapman/mizu/internal/post"
	"github.com/nchapman/mizu/internal/render"
	mizuserver "github.com/nchapman/mizu/internal/server"
	"github.com/nchapman/mizu/internal/site"
	"github.com/nchapman/mizu/internal/theme"
	"github.com/nchapman/mizu/internal/webmention"
)

func main() {
	cfgPath := flag.String("config", "config.yml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	posts, err := post.NewStore(cfg.Paths.Content)
	if err != nil {
		log.Fatalf("posts: %v", err)
	}

	// One SQLite file holds everything durable: users, sessions,
	// feeds/items, mentions, the draft salt. Schema is applied
	// automatically on open via internal/db.
	conn, err := mizudb.Open(cfg.Paths.DB)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	feedStore := feeds.NewStore(conn)
	feedSvc := feeds.NewService(feedStore, cfg.Paths.Subscriptions, cfg.Site.Title)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := feedSvc.LoadFromOPML(ctx); err != nil {
		log.Fatalf("load opml: %v", err)
	}
	poller := feeds.NewPoller(feedStore, cfg.Poller.Interval, cfg.Poller.UserAgent)

	var bg sync.WaitGroup
	bg.Add(1)
	go func() {
		defer bg.Done()
		poller.Run(ctx)
	}()

	wmStore := webmention.NewStore(conn)
	wmSvc := webmention.New(wmStore, cfg.Site.BaseURL)

	// Boot-time sanity check: fail fast if the active theme is broken
	// or missing, rather than letting the first render produce a 500.
	// The pipeline reloads the theme on every build, so this value is
	// discarded; the call is just for its error.
	if _, err := theme.Load(cfg.Theme.Name, themesFS(), cfg.Theme.Settings); err != nil {
		log.Fatalf("theme: %v", err)
	}

	draftSalt, err := render.LoadOrCreateDraftSalt(ctx, conn)
	if err != nil {
		log.Fatalf("draft salt: %v", err)
	}

	pipeline, err := render.NewPipeline(render.Options{
		Sources: &render.SnapshotSources{
			BootCfg:    cfg,
			ConfigPath: *cfgPath,
			ThemesFS:   themesFS(),
			Posts:      posts,
			WM:         wmSvc,
			MediaDir:   cfg.Paths.Media,
			DraftSalt:  draftSalt,
		},
		PublicDir: cfg.Paths.Public,
		HashPath:  filepath.Join(cfg.Paths.State, "build.json"),
	})
	if err != nil {
		log.Fatalf("render pipeline: %v", err)
	}

	// Webmention verifier kicks the pipeline whenever a mention reaches
	// a terminal state (verified or rejected). Verified → page needs
	// to grow the entry. Verified-then-rejected (source removed the
	// link, sender re-notified) → page needs to drop the stale entry.
	wmSvc.OnMentionsChanged(pipeline.Enqueue)

	bg.Add(1)
	go func() {
		defer bg.Done()
		pipeline.Run(ctx)
	}()

	postsDir, draftsDir := posts.Dirs()
	watcher := render.NewWatcher(pipeline,
		[]string{
			postsDir,
			draftsDir,
			filepath.Join(cfg.Paths.Media, "orig"),
			"themes", // disk-resident custom themes; no-op if absent
		},
		[]string{*cfgPath}, // config.yaml — site title, base_url, theme settings all flow through here
	)
	bg.Add(1)
	go func() {
		defer bg.Done()
		if err := watcher.Run(ctx); err != nil {
			log.Printf("render watcher: %v", err)
		}
	}()

	bg.Add(1)
	go func() {
		defer bg.Done()
		wmSvc.RunVerifier(ctx)
	}()

	authSvc, err := auth.New(conn)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	if win, err := authSvc.Window(ctx); err == nil && win.Open {
		log.Printf("first-run setup required — open /admin in a browser within %s to claim this instance",
			auth.SetupWindowDuration)
	}
	bg.Add(1)
	go func() {
		defer bg.Done()
		authSvc.ReapSessions(ctx)
	}()
	mediaStore, err := media.NewStore(cfg.Paths.Media)
	if err != nil {
		log.Fatalf("media: %v", err)
	}
	ipCache := netinfo.NewPublicIPCache()
	// TLSManager is wired below; admin holds it through the
	// TLSController interface so the wizard's enable-tls handler can
	// flip the runner without main.go re-wiring.
	tlsMgr := mizuserver.NewTLSManager(nil, cfg, &bg)
	adminSrv := admin.New(ctx, cfg, *cfgPath, posts, feedSvc, poller, authSvc, mediaStore, wmSvc, ipCache, tlsMgr, adminDistFS())

	r := chi.NewRouter()
	// Deliberately NOT using middleware.RealIP. mizu binds the public
	// listener directly and there is no trusted reverse proxy, so any
	// X-Forwarded-For header is attacker-controlled and would let a
	// caller spoof the client IP — completely defeating the per-IP
	// rate limit.
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(mizuserver.SecureHeaders(cfg.Server.TLS))
	r.Use(mizuserver.RateLimit(cfg.Limits.Rate.Global))

	r.Route("/admin", adminSrv.Routes)
	mediaFS := http.StripPrefix("/media/", http.FileServer(http.Dir(filepath.Join(cfg.Paths.Public, "media"))))
	r.Handle("/media/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Serve the *baked* display variants from public/media (produced
		// by the ImageVariantStage), not the raw originals. Defense in
		// depth: even though uploads are restricted to a small set of
		// raster types, a stale or hand-placed file shouldn't be
		// content-sniffed by the browser into something executable.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Display variants are content-derivatives of their originals;
		// the URL changes when the source filename changes (originals
		// keep their generated base across edits). A day of caching is
		// safe and saves a lot of conditional-GET round trips.
		w.Header().Set("Cache-Control", "public, max-age=86400")
		mediaFS.ServeHTTP(w, req)
	}))

	siteSrv := site.New(cfg, wmSvc, cfg.Paths.Public, authSvc.Configured)
	siteSrv.Routes(r)

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           r,
		ReadHeaderTimeout: cfg.Limits.ReadHeaderTimeout,
		ReadTimeout:       cfg.Limits.ReadTimeout,
		WriteTimeout:      cfg.Limits.WriteTimeout,
		IdleTimeout:       cfg.Limits.IdleTimeout,
		MaxHeaderBytes:    cfg.Limits.MaxHeaderBytes,
	}

	// Hand the router to the TLS manager now that it exists. The
	// plain-HTTP listener on srv keeps running regardless; TLSManager
	// adds the :80/:443 listeners on top when Enable fires (either now
	// from config or later from the wizard).
	tlsMgr.SetHandler(r)

	// Plain listener always runs — it's the bootstrap path the wizard
	// uses (operator visits http://host:8080/admin), and also a LAN
	// fallback once TLS is up.
	bg.Add(1)
	go func() {
		defer bg.Done()
		log.Printf("mizu listening on %s", cfg.Server.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http server: %v", err)
		}
	}()

	if cfg.Server.TLS.Enabled {
		if err := tlsMgr.EnableFromConfig(ctx); err != nil {
			log.Fatalf("tls: %v", err)
		}
	}

	<-ctx.Done()
	log.Print("shutting down")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	var shutWG sync.WaitGroup
	shutWG.Add(2)
	go func() {
		defer shutWG.Done()
		_ = srv.Shutdown(shutCtx)
	}()
	go func() {
		defer shutWG.Done()
		tlsMgr.Shutdown(shutCtx)
	}()
	shutWG.Wait()
	bg.Wait()
	_ = conn.Close()
}
