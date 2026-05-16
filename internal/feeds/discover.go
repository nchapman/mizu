package feeds

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// discoverBodyLimit caps how much of a page we'll read while looking
// for autodiscovery <link> tags. Those live in <head>, so anything
// past a few hundred KiB is wasted bandwidth.
const discoverBodyLimit = 512 * 1024

// ErrNoFeedFound is returned when the URL fetched cleanly but neither
// it nor any <link rel="alternate"> on the page points to a feed.
var ErrNoFeedFound = errors.New("no feed found at URL")

// ErrDiscoverFailed wraps remote-side failures (DNS error, timeout,
// connection refused, non-2xx status, unsupported input). Admin maps
// it to a 400 so operator-input mistakes don't read as server bugs.
var ErrDiscoverFailed = errors.New("could not reach URL")

// Discover resolves an operator-supplied URL to a feed URL. The input
// may be a bare host (news.ycombinator.com), an HTML page, or a feed
// itself. Returns the canonical feed URL on success.
func Discover(ctx context.Context, client *http.Client, input string) (string, error) {
	target, err := normalizeDiscoverInput(input)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("%w: build request: %v", ErrDiscoverFailed, err)
	}
	// Some sites refuse unknown user-agents; identify ourselves.
	req.Header.Set("User-Agent", "mizu-feed-discover/1 (+https://github.com/nchapman/mizu)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/html;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDiscoverFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%w: %s returned status %d", ErrDiscoverFailed, target, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, discoverBodyLimit))
	if err != nil {
		return "", fmt.Errorf("%w: read body: %v", ErrDiscoverFailed, err)
	}
	// resp.Request.URL reflects the post-redirect URL, which is what
	// relative hrefs in the page should resolve against.
	final := resp.Request.URL

	if isFeedContentType(resp.Header.Get("Content-Type")) || looksLikeFeedBody(body) {
		return final.String(), nil
	}

	href, ok := findAutodiscoveryLink(body)
	if !ok {
		return "", ErrNoFeedFound
	}
	ref, err := url.Parse(href)
	if err != nil {
		return "", fmt.Errorf("parse discovered href %q: %w", href, err)
	}
	return final.ResolveReference(ref).String(), nil
}

// normalizeDiscoverInput accepts bare hostnames by defaulting to https.
// Inputs with any non-http(s) scheme are rejected outright so that
// "ftp://x" doesn't get turned into "https://ftp://x" and fail
// downstream with a confusing parse/DNS error.
func normalizeDiscoverInput(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw, nil
	}
	if strings.Contains(raw, "://") {
		return "", fmt.Errorf("%w: unsupported scheme in %q", ErrDiscoverFailed, raw)
	}
	if raw == "" {
		return "", fmt.Errorf("%w: empty URL", ErrDiscoverFailed)
	}
	return "https://" + raw, nil
}

func isFeedContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	switch ct {
	case "application/rss+xml", "application/atom+xml", "application/feed+json":
		return true
	}
	return false
}

// atomFeedTagRE matches an opening <feed ...> element whose attribute
// list contains the Atom namespace URL. Requiring the namespace inside
// the tag (rather than anywhere in the document) avoids false positives
// on HTML pages that happen to mention the Atom namespace in prose.
var atomFeedTagRE = regexp.MustCompile(`(?is)<feed[^>]*http://www\.w3\.org/2005/atom`)

// looksLikeFeedBody sniffs the first chunk of the response for an RSS,
// Atom, or RDF root element. Catches feeds served with generic
// application/xml or text/xml content types.
func looksLikeFeedBody(body []byte) bool {
	b := strings.TrimLeft(string(body), "\xef\xbb\xbf \t\r\n")
	if len(b) > 4096 {
		b = b[:4096]
	}
	low := strings.ToLower(b)
	if strings.Contains(low, "<rss") || strings.Contains(low, "<rdf:rdf") {
		return true
	}
	return atomFeedTagRE.MatchString(low)
}

// findAutodiscoveryLink walks the parsed HTML for
// <link rel="alternate" type="application/rss+xml|atom+xml" href="..."/>.
// RSS is preferred over Atom when both are present.
func findAutodiscoveryLink(body []byte) (string, bool) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	var rss, atom string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "link") {
			var rel, typ, href string
			for _, a := range n.Attr {
				switch strings.ToLower(a.Key) {
				case "rel":
					rel = strings.ToLower(a.Val)
				case "type":
					typ = strings.ToLower(a.Val)
				case "href":
					href = a.Val
				}
			}
			// rel is a space-separated token list per HTML spec.
			if href != "" && containsToken(rel, "alternate") {
				switch typ {
				case "application/rss+xml":
					if rss == "" {
						rss = href
					}
				case "application/atom+xml":
					if atom == "" {
						atom = href
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if rss != "" {
		return rss, true
	}
	if atom != "" {
		return atom, true
	}
	return "", false
}

func containsToken(list, want string) bool {
	for _, tok := range strings.Fields(list) {
		if tok == want {
			return true
		}
	}
	return false
}
