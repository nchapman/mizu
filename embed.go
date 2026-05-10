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

// themesEmbed snapshots the public-site themes so the binary can
// render pages without any files next to it. The shipped "default"
// theme always lives here; operators can add more themes on disk at
// ./themes/<name>/ and select one via cfg.Theme.Name.
//
// The `all:` prefix mirrors the admin/dist directive so dotfiles inside
// a theme (e.g. .keep) aren't silently dropped by the default glob.
//
//go:embed all:themes
var themesEmbed embed.FS

func themesFS() fs.FS {
	sub, err := fs.Sub(themesEmbed, "themes")
	if err != nil {
		log.Fatalf("themes embed: %v", err)
	}
	return sub
}
