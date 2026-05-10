# Handoff: Extract Liquid parser into a standalone Go module

## Goal

Extract the Liquid template parser currently living at
`/Users/nchapman/Code/rugby/stdlib/template` into its own standalone Go
module so it can be imported by `github.com/nchapman/repeat` (and others).

Repository name: **`go-liquid`** (so the import path is
`github.com/nchapman/go-liquid`).

## Why

`repeat` wants to ship a themes feature for its public site. Go's
`html/template` is fine for the maintainer but unfriendly for theme
authors. Liquid is familiar to anyone who's touched Jekyll, Hugo, or
Shopify themes. The existing Go Liquid options (`osteele/liquid`,
`karlseguin/liquid`) are either under-maintained or missing features.
The rugby in-tree parser is more capable than either and is already
ours, so extracting it is the right move.

## What's there today

Source: `/Users/nchapman/Code/rugby/stdlib/template/`

- ~5,300 LOC, pure Go, stdlib only (`fmt`, `html`, `math`, `reflect`,
  `strings`, `time`, `unicode/utf8`, `cmp`, `slices`, `maps`).
- Clean public API: `Parse`, `MustParse`, `Render`, `MustRender`,
  `Template.Render`, `Template.AST`.
- `Template.AST()` exposes the parsed nodes — used by the rugby compiler
  for compile-time template processing. Keep this working.
- Tag coverage: `if`/`elsif`/`else`/`unless`, `case`/`when`, `for` with
  `forloop` + `limit`/`offset`/`reversed`, ranges `(1..5)`, `break`,
  `continue`, `for…else`, `assign`, `capture`, `comment`, `raw`,
  `cycle`.
- Filter coverage: ~60 filters (string, array, math, utility) including
  `where`, `find`, `map`, `sort_natural`, `date` with strftime codes,
  `default`, `truncatewords`, `slice`, full math suite.
- Tests pass: `cd /Users/nchapman/Code/rugby/stdlib/template && go test
  ./...` — 20 test functions, runs clean.

The package's `README.md` documents the syntax and is worth porting as
the new repo's README.

## Extraction plan

### 1. Create the new repo

```bash
mkdir -p /Users/nchapman/Code/go-liquid
cd /Users/nchapman/Code/go-liquid
git init
go mod init github.com/nchapman/go-liquid
```

### 2. Copy the source

Copy every `.go` file (including `_test.go`) from
`/Users/nchapman/Code/rugby/stdlib/template/` to the root of the new
repo. Copy the `README.md` too (you'll edit it).

```bash
cp /Users/nchapman/Code/rugby/stdlib/template/*.go .
cp /Users/nchapman/Code/rugby/stdlib/template/README.md .
```

### 3. Rewrite the package doc + README

- The package comment in `template.go` references "Rugby programs" — rewrite
  it as a generic Go library doc. Drop the `Rugby:` syntax annotations
  scattered throughout exported docs (they're examples in a different
  language and won't make sense to Go users).
- The README's "Import" and "Quick Start" sections are written for
  rugby's import syntax. Rewrite as a Go example using
  `github.com/nchapman/go-liquid`.
- Keep the syntax reference / filter table sections intact — they're
  language-level docs and apply unchanged.

### 4. Verify it stands alone

```bash
go vet ./...
go test ./... -count=1
```

Should pass with zero changes to the actual code. If anything imports
something rugby-specific, the build will tell you (I checked the imports
list and didn't see any, but verify).

### 5. Add a public filter-registration API

Today filters live in a private package-level map (`filters.go`).
Library users need a way to register custom filters without forking.

Add to `filters.go` (or a new `register.go`):

```go
// Filter is the signature for a custom filter function.
// input is the value being filtered; args are the colon/comma-separated
// arguments from the template (e.g. {{ x | foo: "a", 2 }} → args = ["a", 2]).
type Filter func(input any, args ...any) (any, error)

// RegisterFilter installs a custom filter. It overrides any built-in of
// the same name. Not safe for concurrent use with rendering — register
// filters at startup before the first Render call.
func RegisterFilter(name string, fn Filter) {
    filters[name] = fn
}
```

Match the signature to whatever the existing internal filter functions
use. If they're not all the same shape, normalise them to a single
public type — that's the API surface that matters most.

### 6. Wire up CI + tooling

- Add a minimal `.github/workflows/ci.yml` that runs `go vet` and
  `go test ./... -race` on push/PR for Go 1.22+.
- Add a `LICENSE` (MIT or whatever rugby uses).
- Add a `.gitignore` (just `*.test`, `coverage.out`).

### 7. Initial commit + tag

```bash
git add -A
git commit -m "Initial extraction from github.com/nchapman/rugby"
git remote add origin git@github.com:nchapman/go-liquid.git
git push -u origin main
git tag v0.1.0
git push --tags
```

GitHub repo doesn't have to exist yet — push when it does.

### 8. Update rugby to depend on the extracted module

This part is optional for this handoff but worth noting so we don't
fork: in `/Users/nchapman/Code/rugby`, replace the in-tree
`stdlib/template` package with a thin shim that re-exports from
`github.com/nchapman/go-liquid` and adds the rugby-specific bits
(`Rugby:` doc strings, integration with rugby's value system). Or
delete `stdlib/template` and have rugby's compiler import the new
module directly.

Confirm with the user before touching rugby.

## Known gaps the new module will need (not blocking extraction)

These are documented here so they show up in the new repo's TODO/issues
once it exists. Don't try to implement them as part of the extraction —
do extraction first, then iterate.

1. **`{% include %}` / `{% render %}` partials.** Required for theming.
   Needs a pluggable loader interface so the host app controls where
   partials come from (filesystem, embed.FS, etc.):

   ```go
   type Loader interface {
       Load(name string) (string, error)
   }

   func (t *Template) WithLoader(l Loader) *Template
   ```

2. **Source positions in errors.** `template_test.go` covers correctness
   but error messages need line/column for theme authors to diagnose
   mistakes. Lexer should track positions; parser/evaluator errors
   should carry them.

3. **Render limits.** Loop iteration cap, render depth cap, output size
   cap — minimum sandboxing for any host that might run user-supplied
   themes (`repeat` will).

4. **Web-friendly filters.** `url_encode`, `url_decode`, `escape_once`,
   `strip_html`, `markdownify` (probably as an opt-in registration so
   the dependency is the host's choice).

5. **Concurrency review.** Confirm `Template.Render` is safe to call
   concurrently on the same `*Template` (parsed AST should be
   read-only). Document it in the package doc once verified.

## Acceptance criteria for this handoff

- [ ] `github.com/nchapman/go-liquid` exists as its own module
- [ ] `go test ./... -race` passes
- [ ] Package doc + README rewritten for Go consumers
- [ ] Public `RegisterFilter` API exists
- [ ] CI runs on push
- [ ] `v0.1.0` tagged
- [ ] (Optional) PR to rugby switching to the extracted module

Don't bundle the "known gaps" work into this PR — extraction should be a
clean lift-and-rename so it's easy to review and bisect.
