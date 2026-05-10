package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/nchapman/mizu/internal/admin"
	"github.com/nchapman/mizu/internal/auth"
	"github.com/nchapman/mizu/internal/config"
	"github.com/nchapman/mizu/internal/feeds"
	"github.com/nchapman/mizu/internal/media"
	"github.com/nchapman/mizu/internal/post"
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
	postWatcher := post.NewWatcher(posts)

	feedStore, err := feeds.OpenStore(cfg.Paths.Cache)
	if err != nil {
		log.Fatalf("feed store: %v", err)
	}

	feedSvc := feeds.NewService(feedStore, cfg.Paths.Subscriptions, cfg.Site.Title)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := feedSvc.LoadFromOPML(ctx); err != nil {
		log.Fatalf("load opml: %v", err)
	}
	poller := feeds.NewPoller(feedStore, cfg.Poller.Interval, cfg.Poller.UserAgent)

	// Track the poller goroutine so we can wait for it to drain before
	// closing the database. Otherwise an in-flight PollOne can hit a
	// closed connection on shutdown.
	var bg sync.WaitGroup
	bg.Add(1)
	go func() {
		defer bg.Done()
		poller.Run(ctx)
	}()

	bg.Add(1)
	go func() {
		defer bg.Done()
		if err := postWatcher.Run(ctx); err != nil {
			log.Printf("post watcher: %v", err)
		}
	}()

	wmStore, err := webmention.OpenStore(cfg.Paths.Cache)
	if err != nil {
		log.Fatalf("webmention store: %v", err)
	}
	wmLog, err := webmention.NewLogger(cfg.Paths.State)
	if err != nil {
		log.Fatalf("webmention log: %v", err)
	}
	wmSvc := webmention.New(wmStore, wmLog, cfg.Site.BaseURL)
	bg.Add(1)
	go func() {
		defer bg.Done()
		wmSvc.RunVerifier(ctx)
	}()

	activeTheme, err := theme.Load(cfg.Theme.Name, themesFS(), cfg.Theme.Settings)
	if err != nil {
		log.Fatalf("theme: %v", err)
	}
	siteSrv, err := site.New(cfg, posts, wmSvc, activeTheme)
	if err != nil {
		log.Fatalf("site: %v", err)
	}
	authSvc, err := auth.New(cfg.Paths.State)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	if t := authSvc.SetupToken(); t != "" {
		log.Printf("first-run setup required — visit %s/admin and use this one-time token: %s",
			cfg.Site.BaseURL, t)
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
	adminSrv := admin.New(ctx, cfg, posts, feedSvc, poller, authSvc, mediaStore, wmSvc, adminDistFS())

	r := chi.NewRouter()
	// Deliberately NOT using middleware.RealIP. mizu binds the public
	// listener directly and there is no trusted reverse proxy, so any
	// X-Forwarded-For header is attacker-controlled and would let a
	// caller spoof the client IP — completely defeating the per-IP
	// rate limit. Trust r.RemoteAddr instead. Restore RealIP only when
	// you put mizu behind a proxy whose XFF you trust.
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(mizuserver.SecureHeaders(cfg.Server.TLS))
	r.Use(mizuserver.RateLimit(cfg.Limits.Rate.Global))

	r.Route("/admin", adminSrv.Routes)
	mediaFS := http.StripPrefix("/media/", http.FileServer(http.Dir(cfg.Paths.Media)))
	r.Handle("/media/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Defense in depth: even though uploads are restricted to a small
		// set of raster types, a stale or hand-placed file shouldn't be
		// content-sniffed by the browser into something executable.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mediaFS.ServeHTTP(w, req)
	}))
	siteSrv.Routes(r)

	srv := &http.Server{
		Handler:           r,
		ReadHeaderTimeout: cfg.Limits.ReadHeaderTimeout,
		ReadTimeout:       cfg.Limits.ReadTimeout,
		WriteTimeout:      cfg.Limits.WriteTimeout,
		IdleTimeout:       cfg.Limits.IdleTimeout,
		MaxHeaderBytes:    cfg.Limits.MaxHeaderBytes,
	}

	var tlsRunner *mizuserver.TLSRunner
	if cfg.Server.TLS.Enabled {
		tlsRunner, err = mizuserver.StartTLS(ctx, srv, cfg, &bg)
		if err != nil {
			log.Fatalf("tls: %v", err)
		}
	} else {
		srv.Addr = cfg.Server.Addr
		bg.Add(1)
		go func() {
			defer bg.Done()
			log.Printf("mizu listening on %s", cfg.Server.Addr)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("http server: %v", err)
			}
		}()
	}

	<-ctx.Done()
	log.Print("shutting down")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	// Drain both servers concurrently so each gets the full deadline
	// rather than whatever's left after the other finishes draining.
	var shutWG sync.WaitGroup
	shutWG.Add(2)
	go func() {
		defer shutWG.Done()
		_ = srv.Shutdown(shutCtx)
	}()
	go func() {
		defer shutWG.Done()
		tlsRunner.Shutdown(shutCtx)
	}()
	shutWG.Wait()
	bg.Wait()
	_ = feedStore.Close()
	_ = wmStore.Close()
}
