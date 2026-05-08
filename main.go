package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/nchapman/repeat/internal/admin"
	"github.com/nchapman/repeat/internal/config"
	"github.com/nchapman/repeat/internal/feeds"
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
	defer feedStore.Close()

	feedSvc := feeds.NewService(feedStore, cfg.Paths.Subscriptions, cfg.Site.Title)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := feedSvc.LoadFromOPML(ctx); err != nil {
		log.Fatalf("load opml: %v", err)
	}
	poller := feeds.NewPoller(feedStore, cfg.Poller.Interval, cfg.Poller.UserAgent)
	go poller.Run(ctx)

	siteSrv, err := site.New(cfg, posts)
	if err != nil {
		log.Fatalf("site: %v", err)
	}
	adminSrv := admin.New(ctx, cfg, posts, feedSvc, poller)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Route("/admin", adminSrv.Routes)
	r.Handle("/media/*", http.StripPrefix("/media/", http.FileServer(http.Dir(cfg.Paths.Media))))
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
}
