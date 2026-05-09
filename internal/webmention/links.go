package webmention

import (
	"strings"

	"golang.org/x/net/html"
)

// extractLinks returns the unique http(s) hrefs found in the rendered
// HTML, preserving discovery order. Used by SendForPost to find
// candidate webmention targets.
func extractLinks(rendered string) []string {
	doc, err := html.Parse(strings.NewReader(rendered))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key != "href" {
					continue
				}
				v := strings.TrimSpace(a.Val)
				if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
					continue
				}
				if seen[v] {
					continue
				}
				seen[v] = true
				out = append(out, v)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}
