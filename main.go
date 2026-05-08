package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/nchapman/repeat/internal/admin"
	"github.com/nchapman/repeat/internal/config"
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

	siteSrv, err := site.New(cfg, posts)
	if err != nil {
		log.Fatalf("site: %v", err)
	}
	adminSrv := admin.New(cfg, posts)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Route("/admin", adminSrv.Routes)
	r.Handle("/media/*", http.StripPrefix("/media/", http.FileServer(http.Dir(cfg.Paths.Media))))
	siteSrv.Routes(r)

	log.Printf("repeat listening on %s", cfg.Server.Addr)
	if err := http.ListenAndServe(cfg.Server.Addr, r); err != nil {
		log.Fatal(err)
	}
}
