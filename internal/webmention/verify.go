package webmention

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/net/html"
)

// MaxSourceBodyBytes caps the source page we fetch during verification.
// 2 MiB is well above any reasonable blog post; gives some headroom
// for inline base64 images on a single-page site.
const MaxSourceBodyBytes = 2 << 20 // 2 MiB

// ErrLinkNotFound means the source page was fetched successfully but
// did not contain an <a>/<link> with href equal to target.
var ErrLinkNotFound = errors.New("source does not link to target")

// Verify fetches source and confirms it contains a hyperlink to
// target. Returns nil on success; ErrLinkNotFound if no such link
// exists; any other error if the fetch itself failed.
func Verify(ctx context.Context, c *http.Client, source, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/html, */*;q=0.1")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		// Per the spec, a source that's gone removes the mention.
		return ErrLinkNotFound
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("source returned %d", resp.StatusCode)
	}
	doc, err := html.Parse(io.LimitReader(resp.Body, MaxSourceBodyBytes))
	if err != nil {
		return fmt.Errorf("parse source: %w", err)
	}
	if hasLinkTo(doc, target) {
		return nil
	}
	return ErrLinkNotFound
}

// hasLinkTo walks the parsed document and returns true if any <a>,
// <link>, <img>, or <video> href/src equals target. Microformats
// processing for richer attribution lives outside the verify path —
// here we only confirm the link relationship.
func hasLinkTo(n *html.Node, target string) bool {
	if n.Type == html.ElementNode {
		var attr string
		switch n.Data {
		case "a", "link":
			attr = "href"
		case "img", "video", "audio", "source":
			attr = "src"
		}
		if attr != "" {
			for _, a := range n.Attr {
				if a.Key == attr && urlEqual(a.Val, target) {
					return true
				}
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hasLinkTo(c, target) {
			return true
		}
	}
	return false
}

// urlEqual compares two URLs literally after trimming trailing slashes
// and ignoring fragment differences. We deliberately don't normalize
// scheme/case beyond what the strings already are — operators who
// want to accept http<->https aliases can configure their reverse
// proxy to canonicalize.
func urlEqual(a, b string) bool {
	a = strings.TrimSuffix(stripFragment(a), "/")
	b = strings.TrimSuffix(stripFragment(b), "/")
	return a == b
}

func stripFragment(u string) string {
	if i := strings.Index(u, "#"); i >= 0 {
		return u[:i]
	}
	return u
}
