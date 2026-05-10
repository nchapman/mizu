package render

import (
	"context"
	"sort"
	"strings"
)

// ThemeAssetStage walks the active theme's assets/ subtree and emits
// every file under public/assets/<orig-path>. CSS is rewritten so its
// `url(/assets/...)` references inherit content-hash cachebusters from
// the same hash table — keeping fonts/images cached as long as the
// stylesheet that points at them.
//
// Files are emitted at their plain paths (not in hashed directories);
// the cachebuster lives in the URL query stamped into HTML by the
// asset_url filter. The HTTP wrapper sets long-immutable Cache-Control
// when ?v= is present and a short max-age otherwise.
type ThemeAssetStage struct{}

func (ThemeAssetStage) Name() string { return "theme_asset" }

func (ThemeAssetStage) Build(_ context.Context, snap *Snapshot) ([]Output, error) {
	resolve := themeAssetURL(snap)

	// Iterate the snapshot's precomputed table so every stage (this
	// one + the HTML stages that resolve asset_url) sees the same
	// file set. Bytes were read once during snapshot build; reuse
	// them rather than re-opening the FS.
	files := make([]string, 0, len(snap.Assets))
	for name := range snap.Assets {
		files = append(files, name)
	}
	sort.Strings(files)

	out := make([]Output, 0, len(files))
	for _, name := range files {
		body := snap.Assets[name].Body
		if strings.HasSuffix(name, ".css") {
			body = rewriteCSS(body, resolve)
		}
		out = append(out, Output{Path: "assets/" + name, Body: body})
	}
	return out, nil
}
