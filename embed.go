package main

import (
	"embed"
	"io/fs"
	"log"
)

// adminDistEmbed is a snapshot of the built React admin (admin/dist),
// shipped inside the binary so a deployment doesn't need a separate
// asset directory next to the executable.
//
// Build prerequisite: admin/dist must exist before `go build` runs —
// produced by `cd admin && npm run build` (or `make build`, which
// orchestrates the order). Fresh checkouts that haven't built the
// admin will fail at compile time with a clear embed error.
//
// The `all:` prefix includes hashed asset files whose names start
// with `_` or `.`, which the default embed glob would skip.
//
//go:embed all:admin/dist
var adminDistEmbed embed.FS

// adminDistFS strips the leading "admin/dist/" so callers serve files
// at their public paths (e.g. "index.html", "assets/...").
func adminDistFS() fs.FS {
	sub, err := fs.Sub(adminDistEmbed, "admin/dist")
	if err != nil {
		log.Fatalf("admin dist embed: %v", err)
	}
	return sub
}

// templatesEmbed snapshots the public-site HTML templates so the
// binary can render index/post pages without any files next to it.
// Like adminDistEmbed, an on-disk override (cfg.Paths.Templates) wins
// when present — so themes can be iterated without rebuilds.
//
//go:embed templates
var templatesEmbed embed.FS

func templatesFS() fs.FS {
	sub, err := fs.Sub(templatesEmbed, "templates")
	if err != nil {
		log.Fatalf("templates embed: %v", err)
	}
	return sub
}
