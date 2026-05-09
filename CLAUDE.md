# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`repeat` is a self-hosted, single-user microblog and feed reader. One operator runs one instance: it publishes their writing as HTML + RSS at the public root, and acts as their feed reader at `/admin`. Stack: Go 1.25 backend (chi router, html/template, modernc.org/sqlite — pure Go, no cgo), React 18 + Vite + TS admin SPA.

## Commands

- `make check` — full pipeline: lint (gofmt, `go vet`, staticcheck, ESLint, `tsc --noEmit`), tests, build. **Run this before committing — there is no CI yet, so this is your gate.**
- `make build` — `go build ./...` followed by `cd admin && npm run build`. Use this instead of bare `go build` because `embed.go` requires `admin/dist/` to exist.
- `make fmt` — `gofmt -w .` plus `eslint --fix`.
- `make test` — runs `go test ./...` and then `cd admin && npm test` (vitest, single run). Both must pass.
- `go test ./internal/post/... -run TestNameSubstring -v` — single Go package / pattern.
- `cd admin && npm run test:watch` — vitest in watch mode for iterating on a single component test.
- `cd admin && npm run test:coverage` — v8 coverage report for the admin SPA.
- `cd admin && npm run dev` — Vite dev server on `:5173` proxying `/admin/api/*` to Go on `:8080`. Use this for live admin UI iteration; the Go binary serves the embedded admin in production.

The `make` targets (and the canonical build order) live in `Makefile`. Do not reach for `go build` directly — see the embed gotcha below.

## Architecture: two storage layers, two directions

The system has two halves with deliberately different storage shapes. Understanding this split is the most useful thing to internalize:

**Outbound (publishing — what the operator writes):**
- Markdown files with YAML frontmatter are the source of truth. `content/posts/*.md` (published) and `content/drafts/*.md` (unpublished, no public URL).
- No database for posts. The in-memory index in `internal/post/Store` is rebuilt from disk on startup and on `Reload()` (fired by the fsnotify watcher).
- Post slugs are **frozen at create time** in frontmatter so editing a title never changes the URL. Legacy posts without `slug:` get one synthesized from their pre-edit title on first `Update`.
- Atomic file writes throughout: temp + rename, with `Sync()` before rename for files that matter.

**Inbound (feed reader — feeds the operator follows):**
- `subscriptions.opml` is the durable, portable source of truth for the subscription list.
- `cache/repeat.db` (SQLite) is a regeneratable cache of fetched items and read state. **Deleting the cache is safe** — the next poll repopulates from OPML.

**Webmentions** repeat the same pattern: `state/webmentions.log.jsonl` is the durable archive, `cache/webmentions.db` is the queryable index. The DB can be rebuilt from the log.

## Architecture: cross-cutting rules

**SSRF**: every fetch of an operator-supplied URL goes through `internal/safehttp.NewClient()`. It resolves DNS itself and pins the dial to the resolved IP, blocking loopback / RFC-1918 / link-local / multicast / unspecified at dial time. Use this client — never `http.DefaultClient` — for feed polls, webmention source/target fetches, oembed lookups, etc. DNS rebinding mitigation is partial; documented in `safehttp.go`.

**Auth**: bcrypt hash on disk at `state/auth.json` (cost 12, atomic write, 0o600). Sessions live in memory; cookie is `repeat_session`, SameSite=Lax, HttpOnly, 30-day TTL. Public endpoints under `/admin/api/`: `me`, `setup`, `login`, `logout`. Everything else is gated by `auth.Middleware` via a `r.Group` in `internal/admin/admin.go`. First-run uses a one-time setup token printed to stdout, compared in constant time.

**Single-binary deploy**: `admin/dist/` (built React) and `templates/` are embedded via `go:embed` in `embed.go` at the project root (the embed directives are relative to the source file). Both have **disk overrides** via `cfg.Paths.AdminDist` / `cfg.Paths.Templates` — when the directory exists with the expected entry file (`index.html` or `base.html`), it wins; otherwise the embedded copy is served. This keeps the dev workflow flexible while making production a single self-contained binary.

**fsnotify**: `internal/post/watcher.go` reloads the post store on `.md` changes in `posts/` or `drafts/`, debounced 200 ms. The watcher fires for repeat's own writes too — harmless, since `Reload()` is idempotent. The debounce uses a single-goroutine `time.After` loop in a `select`, **not** `time.AfterFunc + Reset` (which has a documented race where a queued fire can run concurrently with a reschedule). Don't reintroduce that pattern.

## Architecture: package map

- `main.go` — wires everything; constructs every service, runs goroutines under a `sync.WaitGroup` so shutdown drains cleanly. New background workers must register with `bg.Add(1)` and `defer bg.Done()`.
- `embed.go` — `//go:embed` directives for `admin/dist` and `templates`. Lives at the root because embed paths are relative to source file.
- `internal/post` — `Store` (posts + drafts), `Watcher` (fsnotify reload). `Reload()` holds the write lock for the full read-and-swap so a concurrent Create can't be silently dropped by the index swap.
- `internal/feeds` — subscriptions, OPML I/O, SQLite cache, polling worker, timeline read API. The poller uses `safehttp` and conditional GET (etag/last-modified). Item content is sanitized at ingest with `bluemonday.UGCPolicy()` so the React side can `dangerouslySetInnerHTML` without re-sanitizing.
- `internal/webmention` — receive endpoint + verifier worker + outbound sender. **Pending mentions are re-enqueued from the DB on startup** — transient fetch errors leave the row at `StatusPending` so the next process retry picks them up. Only `ErrLinkNotFound` (definite "source doesn't link to us") flips to `StatusRejected`.
- `internal/site` — public-site rendering: `/`, `/feed.xml`, `/notes/{id}`, `/{year}/{month}/{day}/{slug}`, `POST /webmention`. Templates parsed via `ParseFS` against the active templates FS.
- `internal/admin` — REST API and SPA serving. Routes under `/admin/api/*`; SPA fallback under `/admin/*` serves the embedded React shell for any unknown path so client-side routing takes over.
- `internal/auth` — password storage, sessions, middleware.
- `internal/media` — image upload + resize. Original kept verbatim at `media/orig/<name>`; display variant at `media/<name>`, capped at 1600px long edge. Type detection is by magic bytes (`http.DetectContentType`), never the client-supplied Content-Type. PNG → PNG passthrough when small, JPEG → JPEG q=85, WebP → JPEG (stdlib can't encode WebP), GIF passthrough (preserves animation). SVG is intentionally rejected (XSS surface).
- `internal/safehttp` — the SSRF-safe HTTP client. Used by `feeds` and `webmention`.
- `internal/config` — YAML config loader, creates writable directories on startup.

## Build gotchas

- `//go:embed all:admin/dist` requires the directory to exist with at least one file. A committed `admin/dist/.keep` covers fresh checkouts; `npm run build` ends with `touch dist/.keep` because Vite's `emptyOutDir: true` clears the directory on every build. Both `.gitignore` files have negation rules to keep `.keep` tracked. Do not flip `emptyOutDir` to false (stale assets would accumulate).
- `go build` from a cold clone before `npm run build` will fail with an embed error. Run `make build` instead, or just `cd admin && npm install && npm run build` once.
- `modernc.org/sqlite` is pure Go. The Dockerfile builds with `CGO_ENABLED=0` and ships a fully static binary on Alpine.

## Testing

**Go tests** live next to source as `_test.go`. Conventions used across the suite:

- `t.TempDir()` for every store/file path. Real SQLite, real disk, real chi router — internal collaborators are not mocked. Refactoring internals shouldn't break tests.
- HTTP-touching services accept dependency injection so loopback `httptest` servers don't trip `safehttp`. `webmention.Service.http` and `feeds.Poller.http` are field-swappable; tests assign `http.DefaultClient`. `feeds.Service.validate` is a swappable validator field with the same intent.
- **Cross-package test seam pattern**: `internal/feeds/testhelpers.go` exposes `Service.SetValidateForTest` and `SetPollerHTTPForTest`. The file is intentionally NOT `_test.go` — `_test.go` files aren't visible from other packages, and the `admin` tests need to drive `feeds` through its public surface. Keep the surface minimal; don't grow it to expose unrelated internals.
- The webmention package follows the same pattern via `s.http = http.DefaultClient` directly (see `webmention_test.go:newService`).
- `internal/media/extra_test.go` carries an inlined 64-byte WebP fixture (`minimalWebP`). Stdlib has no WebP encoder, so the bytes were generated once via `cwebp`. The test self-verifies via `http.DetectContentType` so a corrupt fixture fails loudly.
- The watcher test exposes a `ready` channel (`Watcher.ready`) so the test can wait for fsnotify subscriptions to register before writing files. Without it, the first write races the watcher's `Add()` calls.

**TS tests** live next to source as `*.test.ts(x)`. Conventions:

- Vitest + React Testing Library + jsdom. Setup in `admin/src/test/setup.ts` (jest-dom matchers + RTL `cleanup` after each test).
- **`queueFetch` helper** (`admin/src/test/fetch.ts`) is the canonical way to drive components: pass an array of `{status, body?}` replies, get back a `vi.fn()`. It stubs `globalThis.fetch`, registers its own `afterEach(() => vi.unstubAllGlobals())`, throws on overrun (so missing-stub bugs fail loudly instead of hanging), and handles the spec quirk that `Response` constructors reject empty-string bodies on 204.
- Tests assert against visible text and ARIA roles, not DOM structure or test ids. `dangerouslySetInnerHTML` paths are verified by querying the rendered children (`container.querySelector(".post-rendered em")`) — don't re-implement the sanitizer in test fixtures.
- **Lexical-in-jsdom limitation**: `MarkdownEditor` tests only cover mount, placeholder, hydration from initial markdown, and the imperative handle. Lexical relies on `contenteditable` + `Selection` APIs that jsdom only partially implements, so interactive typing isn't tested in unit form. The comment in `MarkdownEditor.test.tsx` documents the gap; cover those paths via browser smoke testing.
- Stub `globalThis.confirm` directly when a flow calls it (Drafts/Subscriptions destructive actions). Pair with an `afterEach(() => vi.unstubAllGlobals())` in the same describe — `queueFetch` only owns its own cleanup.

## Style + workflow

- Code-review subagents run from a `/review` skill (see `userSettings:review`). Run after any non-trivial slice; security-reviewer for anything that touches auth, untrusted input, or HTTP fetches.
- Frontmatter regex (`internal/post/post.go`) deliberately consumes up to two newlines after the closing `---` — exactly the convention's separator — and preserves any further leading blanks as authored content. There's a regression test for this; don't relax it back to greedy.
- React composer state lives in `HomeView`. Cross-tab handoffs (e.g., Drafts → composer) go via `Shell.editTarget`, consumed once via `useEffect` and cleared with `onEditConsumed`.
- HTML-rendered post bodies use `dangerouslySetInnerHTML` for sanitized feed content. The trust boundary is `bluemonday` at ingest, not at render.
