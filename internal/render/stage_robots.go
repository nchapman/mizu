package render

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// RobotsStage emits robots.txt — keep crawlers out of /admin/ and
// /_drafts/, point them at the sitemap.
type RobotsStage struct{}

func (RobotsStage) Name() string { return "robots" }

func (RobotsStage) Build(_ context.Context, snap *Snapshot) ([]Output, error) {
	var b bytes.Buffer
	b.WriteString("User-agent: *\n")
	b.WriteString("Disallow: /admin/\n")
	// Deliberately NOT disallowing /_drafts/ here. The HMAC slug is the
	// security primitive — listing the prefix would advertise its
	// existence to every scanner without adding protection that the
	// per-response X-Robots-Tag header doesn't already provide.
	if base := strings.TrimRight(snap.BaseURL, "/"); base != "" {
		fmt.Fprintf(&b, "\nSitemap: %s/sitemap.xml\n", base)
	}
	return []Output{{Path: "robots.txt", Body: b.Bytes()}}, nil
}
