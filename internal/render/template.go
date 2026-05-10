package render

import (
	"fmt"
	"io/fs"
	"net/url"
	"regexp"
	"strings"
	"time"

	liquid "github.com/nchapman/go-liquid"
)

// templateSet bundles a parsed Liquid template per page name plus the
// shared environment that resolves partials against the active theme's
// FS. One templateSet is created per pipeline build; theme reloads
// recreate it.
type templateSet struct {
	pages map[string]*liquid.Template
	env   *liquid.Environment
}

// loadTemplates parses base/index/post from the layered theme FS.
// AssetURL is the resolved URL for an asset path (closure over the
// theme's hashed asset map) — passed in rather than computed inline so
// the templateSet has no knowledge of how the hash is produced.
func loadTemplates(themeFS fs.FS, assetURL func(string) string) (*templateSet, error) {
	env := liquid.NewEnvironment()
	env.RegisterFilter("host_of", func(input any, _ ...any) any {
		s, _ := input.(string)
		return hostOf(s)
	})
	env.RegisterFilter("asset_url", func(input any, _ ...any) any {
		s, ok := input.(string)
		if !ok {
			return ""
		}
		if assetURL == nil {
			return "/assets/" + strings.TrimPrefix(s, "/")
		}
		return assetURL(s)
	})
	env.RegisterFilter("iso8601", func(input any, _ ...any) any {
		if t, ok := input.(time.Time); ok {
			return t.Format(time.RFC3339)
		}
		return input
	})
	env.RegisterFilter("css_value", func(input any, _ ...any) any {
		s, ok := input.(string)
		if !ok {
			return ""
		}
		return cssValue(s)
	})
	if themeFS != nil {
		env.WithLoader(&liquid.FileSystemLoader{FS: themeFS, Ext: ".liquid"})
	}
	pages := map[string]*liquid.Template{}
	for _, name := range []string{"base.liquid", "index.liquid", "post.liquid"} {
		src, err := fs.ReadFile(themeFS, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		t, err := env.Parse(string(src))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		pages[name] = t.WithName(name)
	}
	return &templateSet{pages: pages, env: env}, nil
}

// renderPage executes the named page template and composes the result
// into base.liquid via content_for_layout. Returns the bytes of the
// final HTML. Templates have access to `site`, `theme`, `page_title`
// and whatever's in data.
func (ts *templateSet) renderPage(name, pageTitle string, themeData map[string]any, site any, data map[string]any) ([]byte, error) {
	page, ok := ts.pages[name]
	if !ok {
		return nil, fmt.Errorf("template %s not found", name)
	}
	body, err := page.Render(data)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	out, err := ts.pages["base.liquid"].Render(map[string]any{
		"site":               site,
		"theme":              themeData,
		"page_title":         pageTitle,
		"content_for_layout": body,
	})
	if err != nil {
		return nil, fmt.Errorf("render base.liquid: %w", err)
	}
	return []byte(out), nil
}

// cssSafePattern accepts a small set of value shapes a theme setting
// can legitimately interpolate into a CSS custom property:
//   - hex colors (#abc, #aabbcc, #aabbccdd)
//   - lengths (12px, 1.5rem, 100%, 0.5em, 10vh, 12vw, 8ex, 4ch)
//   - bare numbers (1, 0.5, .25)
//   - rgb()/rgba()/hsl()/hsla() with simple args
//   - "none", "currentColor", "transparent"
//
// Anything else drops to "". Cost of strict: a missing variable. Cost
// of loose: a theme setting closing the declaration and injecting
// arbitrary CSS.
var cssSafePattern = regexp.MustCompile(
	`^(#[0-9a-fA-F]{3,8}` +
		`|-?\d*\.?\d+(px|rem|em|%|vw|vh|vmin|vmax|ex|ch|pt)?` +
		`|(rgb|rgba|hsl|hsla)\([\d,.\s%/]+\)` +
		`|none|currentColor|transparent` +
		`)$`)

func cssValue(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || !cssSafePattern.MatchString(s) {
		return ""
	}
	return s
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Hostname()
}

// cssURLPattern matches `url(/assets/<path>)` references in CSS in the
// three legal quote forms (double, single, none). RE2 has no
// backreferences, hence the alternation. We only rewrite paths under
// /assets/ — data: URIs and external URLs pass through.
var cssURLPattern = regexp.MustCompile(
	`url\(\s*(?:` +
		`"/assets/([^"]+)"` +
		`|'/assets/([^']+)'` +
		`|/assets/([^)\s]+)` +
		`)\s*\)`)

// rewriteCSS rewrites every `url(/assets/...)` reference in src to the
// content-addressed form. resolve is the URL resolver (typically
// assetIndex.URL).
func rewriteCSS(src []byte, resolve func(string) string) []byte {
	return cssURLPattern.ReplaceAllFunc(src, func(match []byte) []byte {
		sub := cssURLPattern.FindSubmatch(match)
		var ref string
		var quote string
		switch {
		case sub[1] != nil:
			ref, quote = string(sub[1]), `"`
		case sub[2] != nil:
			ref, quote = string(sub[2]), `'`
		default:
			ref = string(sub[3])
		}
		// Strip an existing query before lookup; preserve any fragment
		// (legal on font url() refs for subset selection).
		var fragment string
		if i := strings.IndexByte(ref, '#'); i >= 0 {
			fragment = ref[i:]
			ref = ref[:i]
		}
		if i := strings.IndexByte(ref, '?'); i >= 0 {
			ref = ref[:i]
		}
		return []byte("url(" + quote + resolve(ref) + fragment + quote + ")")
	})
}
