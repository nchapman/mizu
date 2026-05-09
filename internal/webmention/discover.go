package webmention

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// MaxDiscoveryBodyBytes caps the response when we're scanning for a
// webmention endpoint declaration. The HTML <head> is what we need;
// we never have to read the whole document.
const MaxDiscoveryBodyBytes = 1 << 20 // 1 MiB

// ErrNoEndpoint means the target page neither sent a Link header nor
// embedded a <link>/<a rel="webmention"> in its HTML.
var ErrNoEndpoint = errors.New("no webmention endpoint advertised")

// Discover finds the webmention endpoint for a target URL, per the
// W3C spec: GET the target, prefer Link header, fall back to in-body
// <link rel="webmention"> or <a rel="webmention">. The returned URL
// is resolved relative to the request's final URL (after redirects).
func Discover(ctx context.Context, c *http.Client, target string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/html, */*;q=0.1")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("target returned %d", resp.StatusCode)
	}

	// 1. Link header(s) — RFC 5988 / 8288.
	for _, raw := range resp.Header.Values("Link") {
		if endpoint, ok := parseLinkHeader(raw, "webmention"); ok {
			return resolve(resp.Request.URL, endpoint), nil
		}
	}

	// 2. Body scan, capped.
	body := io.LimitReader(resp.Body, MaxDiscoveryBodyBytes)
	doc, err := html.Parse(body)
	if err != nil {
		return "", fmt.Errorf("parse target html: %w", err)
	}
	if endpoint, ok := findRelInDoc(doc, "webmention"); ok {
		return resolve(resp.Request.URL, endpoint), nil
	}
	return "", ErrNoEndpoint
}

// parseLinkHeader returns the URL of the first link in raw whose
// rel-types contain rel. Multiple comma-separated values per header
// are supported; quoted parameter values are unwrapped.
//
// Example raw: `<https://example.com/wm>; rel="webmention", <...>; rel="hub"`
func parseLinkHeader(raw, rel string) (string, bool) {
	for _, part := range splitTopLevel(raw, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// URL is the angle-bracketed prefix.
		lt := strings.Index(part, "<")
		gt := strings.Index(part, ">")
		if lt != 0 || gt < 0 {
			continue
		}
		urlPart := part[1:gt]
		params := part[gt+1:]
		for _, p := range strings.Split(params, ";") {
			p = strings.TrimSpace(p)
			if !strings.HasPrefix(strings.ToLower(p), "rel=") {
				continue
			}
			val := strings.TrimPrefix(p[4:], "")
			val = strings.Trim(val, `"`)
			for _, r := range strings.Fields(val) {
				if strings.EqualFold(r, rel) {
					return urlPart, true
				}
			}
		}
	}
	return "", false
}

// splitTopLevel splits raw on sep, treating angle-bracketed substrings
// as atomic. The Link header allows commas inside <...>, which a naive
// split would mishandle.
func splitTopLevel(raw string, sep byte) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				out = append(out, raw[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, raw[start:])
	return out
}

func findRelInDoc(n *html.Node, rel string) (string, bool) {
	if n.Type == html.ElementNode && (n.Data == "link" || n.Data == "a") {
		var href, relAttr string
		for _, a := range n.Attr {
			switch a.Key {
			case "href":
				href = a.Val
			case "rel":
				relAttr = a.Val
			}
		}
		for _, r := range strings.Fields(relAttr) {
			if strings.EqualFold(r, rel) && href != "" {
				return href, true
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if v, ok := findRelInDoc(c, rel); ok {
			return v, ok
		}
	}
	return "", false
}

func resolve(base *url.URL, ref string) string {
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if base == nil {
		return u.String()
	}
	return base.ResolveReference(u).String()
}
