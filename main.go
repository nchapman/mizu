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

	"github.com/nchapman/repeat/internal/admin"
	"github.com/nchapman/repeat/internal/auth"
	"github.com/nchapman/repeat/internal/config"
	"github.com/nchapman/repeat/internal/feeds"
	"github.com/nchapman/repeat/internal/media"
	"github.com/nchapman/repeat/internal/post"
	"github.com/nchapman/repeat/internal/site"
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

	siteSrv, err := site.New(cfg, posts)
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
	adminSrv := admin.New(ctx, cfg, posts, feedSvc, poller, authSvc, mediaStore)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
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

	srv := &http.Server{Addr: cfg.Server.Addr, Handler: r}
	go func() {
		log.Printf("repeat listening on %s", cfg.Server.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	bg.Wait()
	_ = feedStore.Close()
}
