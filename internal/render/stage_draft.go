package render

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"
	"time"

	"github.com/nchapman/mizu/internal/post"
)

// DraftStage renders one preview page per draft at an unguessable
// salted slug under /_drafts/. The slug is HMAC-SHA256(salt, draft.id),
// base32-encoded and truncated to 16 chars (80 bits) — far beyond
// guessable. Drafts are never listed anywhere; the operator opens them
// from the admin UI by computing the same slug.
type DraftStage struct{}

func (DraftStage) Name() string { return "draft" }

// draftView gives the post.liquid template what it needs to render a
// draft as if it were a post. The template references Title, Date, HTML.
type draftView struct {
	*post.Draft
	HTML string
	Date time.Time
}

func (s DraftStage) Build(_ context.Context, snap *Snapshot) ([]Output, error) {
	if len(snap.DraftSalt) == 0 {
		// Without a salt we'd produce predictable URLs; treat as a
		// hard skip rather than emit anything.
		return nil, nil
	}
	out := make([]Output, 0, len(snap.Drafts))
	var firstErr error
	for _, d := range snap.Drafts {
		body, err := s.renderOne(snap.Templates, snap.ThemeData, snap, d)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, Output{
			Path: "_drafts/" + DraftSlug(snap.DraftSalt, d.ID) + "/index.html",
			Body: body,
		})
	}
	return out, firstErr
}

func (DraftStage) renderOne(tpl *templateSet, themeData map[string]any, snap *Snapshot, d *post.Draft) ([]byte, error) {
	html, err := d.RenderHTML()
	if err != nil {
		return nil, fmt.Errorf("render draft markdown %s: %w", d.ID, err)
	}
	pageTitle := "Draft · " + snap.Site.Title
	if d.Title != "" {
		pageTitle = "Draft: " + d.Title + " · " + snap.Site.Title
	}
	return tpl.renderPage("post.liquid", pageTitle, themeData, snap.Site, map[string]any{
		"site":  snap.Site,
		"theme": themeData,
		"post":  draftView{Draft: d, HTML: html, Date: d.Created},
		// Drafts never carry mentions — they're not on the public
		// post.AbsoluteURL graph.
		"mentions": []mentionView{},
	})
}

// draftSlugBytes is how many bytes of the HMAC output land in the URL.
// 10 bytes = 80 bits, far beyond online-guessable; base32 encodes to
// exactly 16 chars with no padding.
const draftSlugBytes = 10

// DraftSlug derives the unguessable URL component for a draft id.
// Exposed so the admin SPA can build preview links the same way.
func DraftSlug(salt []byte, id string) string {
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(id))
	sum := mac.Sum(nil)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:draftSlugBytes])
	return strings.ToLower(enc)
}
