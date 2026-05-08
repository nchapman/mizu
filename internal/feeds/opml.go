package feeds

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
)

// OPML 2.0 minimal subset. We support a flat list and one level of
// category nesting. Deeper nesting is flattened on read.

type opmlDoc struct {
	XMLName xml.Name     `xml:"opml"`
	Version string       `xml:"version,attr"`
	Head    opmlHead     `xml:"head"`
	Body    opmlBody     `xml:"body"`
}

type opmlHead struct {
	Title string `xml:"title"`
}

type opmlBody struct {
	Outlines []opmlOutline `xml:"outline"`
}

type opmlOutline struct {
	Text     string        `xml:"text,attr"`
	Title    string        `xml:"title,attr,omitempty"`
	Type     string        `xml:"type,attr,omitempty"`
	XMLURL   string        `xml:"xmlUrl,attr,omitempty"`
	HTMLURL  string        `xml:"htmlUrl,attr,omitempty"`
	Children []opmlOutline `xml:"outline,omitempty"`
}

// Subscription is a single feed entry as represented in OPML.
type Subscription struct {
	Title    string
	FeedURL  string
	SiteURL  string
	Category string
}

// LoadOPML reads subscriptions from disk. Missing file returns no error and
// an empty slice — first-run convenience.
func LoadOPML(path string) ([]Subscription, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc opmlDoc
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse opml: %w", err)
	}
	var subs []Subscription
	var walk func(parent string, outlines []opmlOutline)
	walk = func(parent string, outlines []opmlOutline) {
		for _, o := range outlines {
			if o.XMLURL != "" {
				subs = append(subs, Subscription{
					Title:    firstNonEmpty(o.Title, o.Text),
					FeedURL:  o.XMLURL,
					SiteURL:  o.HTMLURL,
					Category: parent,
				})
			}
			if len(o.Children) > 0 {
				cat := parent
				if cat == "" {
					cat = firstNonEmpty(o.Text, o.Title)
				}
				walk(cat, o.Children)
			}
		}
	}
	walk("", doc.Body.Outlines)
	return subs, nil
}

// SaveOPML writes subscriptions back to disk, grouping by category. Atomic
// write via temp + rename so a crashed write can't corrupt the file.
func SaveOPML(path, siteTitle string, subs []Subscription) error {
	byCat := map[string][]opmlOutline{}
	var catOrder []string
	for _, s := range subs {
		o := opmlOutline{
			Text:    s.Title,
			Title:   s.Title,
			Type:    "rss",
			XMLURL:  s.FeedURL,
			HTMLURL: s.SiteURL,
		}
		if _, ok := byCat[s.Category]; !ok {
			catOrder = append(catOrder, s.Category)
		}
		byCat[s.Category] = append(byCat[s.Category], o)
	}
	var top []opmlOutline
	for _, cat := range catOrder {
		if cat == "" {
			top = append(top, byCat[cat]...)
			continue
		}
		top = append(top, opmlOutline{Text: cat, Title: cat, Children: byCat[cat]})
	}
	doc := opmlDoc{
		Version: "2.0",
		Head:    opmlHead{Title: siteTitle + " subscriptions"},
		Body:    opmlBody{Outlines: top},
	}
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out := []byte(xml.Header + string(body) + "\n")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}
