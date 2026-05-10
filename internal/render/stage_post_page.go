package render

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nchapman/mizu/internal/post"
	"github.com/nchapman/mizu/internal/webmention"
)

// PostPageStage renders one HTML file per published post — articles
// at /YYYY/MM/DD/slug/index.html, notes at /notes/<id>/index.html.
type PostPageStage struct{}

func (PostPageStage) Name() string { return "post_page" }

// renderedPost mirrors the shape used at request time: an embedded
// *Post promotes Title/Date/Path() into the template scope, plus a
// pre-rendered HTML field so the template doesn't have to call
// RenderHTML.
type renderedPost struct {
	*post.Post
	HTML string
}

type mentionView struct {
	Source     string
	VerifiedAt time.Time
}

func (s PostPageStage) Build(_ context.Context, snap *Snapshot) ([]Output, error) {
	out := make([]Output, 0, len(snap.Posts))
	var firstErr error
	for _, p := range snap.Posts {
		body, err := s.renderOne(snap.Templates, snap.ThemeData, snap, p)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, Output{Path: postOutputPath(p), Body: body})
	}
	return out, firstErr
}

func (PostPageStage) renderOne(tpl *templateSet, themeData map[string]any, snap *Snapshot, p *post.Post) ([]byte, error) {
	html, err := p.RenderHTML()
	if err != nil {
		return nil, fmt.Errorf("render markdown for %s: %w", p.ID, err)
	}
	target := snap.BaseURL + p.Path()
	mentions := snap.Mentions[target]
	views := make([]mentionView, 0, len(mentions))
	for _, m := range mentions {
		// Render-time filter: only verified mentions reach the template.
		// AllVerified should already filter, but be defensive — an
		// unverified URL must never end up in static HTML.
		if m.Status != webmention.StatusVerified {
			continue
		}
		views = append(views, mentionView{Source: m.Source, VerifiedAt: m.VerifiedAt})
	}
	pageTitle := snap.Site.Title
	if p.Title != "" {
		pageTitle = p.Title + " · " + snap.Site.Title
	}
	return tpl.renderPage("post.liquid", pageTitle, themeData, snap.Site, map[string]any{
		"site":     snap.Site,
		"theme":    themeData,
		"post":     renderedPost{Post: p, HTML: html},
		"mentions": views,
	})
}

// postOutputPath maps a post to its output file under PublicDir.
// Articles include a trailing "index.html" so /YYYY/MM/DD/slug/ resolves
// via FileServer's directory-index behavior.
func postOutputPath(p *post.Post) string {
	if p.IsNote() {
		return "notes/" + p.ID + "/index.html"
	}
	rel := strings.TrimPrefix(p.Path(), "/")
	return rel + "/index.html"
}

// themeAssetURL returns the closure templates use to resolve
// `{{ "style.css" | asset_url }}` to the content-addressed URL. Reads
// from snap.AssetHashes — populated once per snapshot so this is a
// pure map lookup.
func themeAssetURL(snap *Snapshot) func(string) string {
	return func(path string) string {
		clean := strings.TrimPrefix(path, "/")
		if clean == "" {
			return "/assets/"
		}
		if v, ok := snap.AssetHashes[clean]; ok && v != "" {
			return "/assets/" + clean + "?v=" + v
		}
		return "/assets/" + clean
	}
}
