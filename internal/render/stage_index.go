package render

import (
	"context"
	"fmt"
)

// PostsPerPage is the homepage page size. Hardcoded — a single-operator
// microblog has no real reason to expose this as a knob.
const PostsPerPage = 20

// IndexStage renders the paginated homepage(s). Page 1 lands at
// index.html; page N (N>1) at page/N/index.html. An empty store still
// produces a single index.html so the FileServer has something to
// answer GET / with.
type IndexStage struct{}

func (IndexStage) Name() string { return "index" }

func (s IndexStage) Build(_ context.Context, snap *Snapshot) ([]Output, error) {
	total := len(snap.Posts)
	totalPages := (total + PostsPerPage - 1) / PostsPerPage
	if totalPages < 1 {
		totalPages = 1
	}

	out := make([]Output, 0, totalPages)
	var firstErr error
	for page := 1; page <= totalPages; page++ {
		body, err := s.renderPage(snap.Templates, snap.ThemeData, snap, page, totalPages)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		path := "index.html"
		if page > 1 {
			path = fmt.Sprintf("page/%d/index.html", page)
		}
		out = append(out, Output{Path: path, Body: body})
	}
	return out, firstErr
}

func (IndexStage) renderPage(tpl *templateSet, themeData map[string]any, snap *Snapshot, page, totalPages int) ([]byte, error) {
	start := (page - 1) * PostsPerPage
	end := start + PostsPerPage
	if end > len(snap.Posts) {
		end = len(snap.Posts)
	}
	slice := snap.Posts[start:end]

	rendered := make([]renderedPost, len(slice))
	for i, p := range slice {
		html, err := p.RenderHTML()
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", p.ID, err)
		}
		rendered[i] = renderedPost{Post: p, HTML: html}
	}

	pageTitle := ""
	if page > 1 {
		pageTitle = fmt.Sprintf("Page %d · %s", page, snap.Site.Title)
	}

	return tpl.renderPage("index.liquid", pageTitle, themeData, snap.Site, map[string]any{
		"site":       snap.Site,
		"theme":      themeData,
		"posts":      rendered,
		"pagination": paginationView(page, totalPages),
	})
}

// paginationView matches the request-time shape: prev/next URLs are
// omitted entirely (not set to "") when there's no link in that
// direction — Liquid considers "" truthy, so a present-but-empty key
// would still render `{% if %}` blocks.
func paginationView(page, totalPages int) map[string]any {
	v := map[string]any{
		"page":        page,
		"total_pages": totalPages,
	}
	switch {
	case page == 2:
		v["prev_url"] = "/"
	case page > 2:
		v["prev_url"] = fmt.Sprintf("/page/%d/", page-1)
	}
	if page < totalPages {
		v["next_url"] = fmt.Sprintf("/page/%d/", page+1)
	}
	return v
}
