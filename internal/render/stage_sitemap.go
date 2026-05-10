package render

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"time"
)

// SitemapStage emits sitemap.xml listing the homepage and every post.
type SitemapStage struct{}

func (SitemapStage) Name() string { return "sitemap" }

func (SitemapStage) Build(_ context.Context, snap *Snapshot) ([]Output, error) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")

	writeURL := func(loc string, lastmod time.Time) {
		buf.WriteString("  <url>\n    <loc>")
		_ = xml.EscapeText(&buf, []byte(loc))
		buf.WriteString("</loc>\n")
		if !lastmod.IsZero() {
			fmt.Fprintf(&buf, "    <lastmod>%s</lastmod>\n", lastmod.UTC().Format("2006-01-02"))
		}
		buf.WriteString("  </url>\n")
	}

	homeLastmod := time.Time{}
	if len(snap.Posts) > 0 {
		homeLastmod = snap.Posts[0].Date
	}
	writeURL(snap.BaseURL+"/", homeLastmod)
	for _, p := range snap.Posts {
		writeURL(snap.BaseURL+p.Path(), p.Date)
	}
	buf.WriteString("</urlset>\n")
	return []Output{{Path: "sitemap.xml", Body: buf.Bytes()}}, nil
}
