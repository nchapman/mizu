# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`mizu` is a self-hosted, single-user microblog and feed reader. One binary publishes the operator's writing as HTML + RSS at the public root, runs the admin SPA at `/admin`, and acts as a feed reader. (The DB schema supports multiple users — sessions, lockout, etc. are all keyed by user — but multi-user UX isn't a goal yet; treat it as a single-operator product.) Stack: Go 1.25 backend (chi router, html/template, modernc.org/sqlite — pure Go, no cgo), React 18 + Vite + TS admin SPA, CertMagic for Let's Encrypt.

Operators install via `make build && ./mizu` or a Docker image, and finish setup through a first-run wizard at `/admin` that writes `config.yml`. `deploy/cloud-init.yaml` + `deploy/docker-compose.yml` provision a fresh VPS end-to-end with Watchtower auto-updates.

## Commands

- `make check` — full pipeline: lint (gofmt, `go vet`, staticcheck, ESLint, `tsc --noEmit`), tests, build. **Run this before committing — CI only builds/publishes the Docker image on version tags (`.github/workflows/docker-publish.yml`); there is no test/lint CI, so `make check` is the gate.**
- `make build` — `go build ./...` followed by `cd admin && npm run build`. Use this instead of bare `go build` because `embed.go` requires `admin/dist/` to exist.
- `make fmt` — `gofmt -w .` plus `eslint --fix`.
- `make test` — runs `go test ./...` then `cd admin && npm test` (vitest, single run). Both must pass.
- `go test ./internal/render/... -run TestNameSubstring -v` — single Go package / pattern.
- `cd admin && npm run test:watch` — vitest in watch mode.
- `cd admin && npm run test:coverage` — v8 coverage for the admin SPA.
- `cd admin && npm run dev` — Vite dev server on `:5173` proxying `/admin/api/*` to Go on `:8080`. Use for live admin UI iteration; the Go binary serves the embedded admin in production.

## Architecture: the big picture

There are **three storage layers**, each with a deliberate role. Get this distinction right or you'll fight the codebase.

**1. Markdown files on disk (source of truth for posts):**
- `content/posts/*.md` (published) and `content/drafts/*.md` (unpublished). Markdown with YAML frontmatter, edited via the composer or any text editor.
- No DB for posts. `internal/post/Store` rebuilds an in-memory index from disk on startup and on `Reload()`. The fsnotify watcher (in `internal/render/watcher.go`) triggers reloads.
- Slugs are **frozen at create time** in frontmatter so editing a title never changes the URL. Legacy posts without `slug:` get one synthesized on first `Update`.
- Atomic writes: temp + `Sync()` + rename.

**2. OPML file on disk (source of truth for subscriptions):**
- `subscriptions.opml` is the durable, portable subscription list. Hand-editable; survives a wiped DB.

**3. One SQLite file (everything else durable):**
- `internal/db.Open` opens **one DB** that holds users, sessions, feed items + read state, webmentions, the per-install draft salt, and login lockout counters. Migrations live in `internal/db/migrations/*.sql` and apply in order via `PRAGMA user_version`.
- Two pools to the same file: `W` is a **single-connection writer pool** (writes serialize at the Go layer; no SQLITE_BUSY churn), `R` is a multi-connection reader pool opened with `query_only`. Services pick the right handle: writes/transactions through `W`, read-only queries through `R`.
- The DB is regeneratable from OPML + the markdown files (you'd lose read state and verified mentions, but the operator can survive that).

**Crucially**, **public-site HTML is not in any of those layers.** See next section.

## Architecture: the render pipeline

The public site is **baked to disk**, not rendered on the request path. Internalize this — it's the largest structural fact about the codebase.

- `internal/render.Pipeline` is a coordinator that runs `Stage`s in order against a `Snapshot` (read-only view of every source: posts, drafts, theme, mentions, media originals, config). The default stage set, in order:
  `ThemeAssetStage`, `ImageVariantStage`, `PostPageStage`, `IndexStage`, `FeedStage`, `SitemapStage`, `RobotsStage`, `DraftStage`.
- Output goes to `cfg.Paths.Public/` (e.g. `index.html`, `feed.xml`, `sitemap.xml`, `2026/05/11/<slug>/index.html`, `media/<name>`, `_drafts/<salted-slug>/index.html`).
- A persisted hash file at `<state>/build.json` lets stages short-circuit when their inputs are unchanged. **Cache invariant:** a cache key MUST be a hash of every input the value depends on. No closures over un-keyed state. There's a comment in `pipeline.go` explaining the bug this rule came from — read it before adding any caching.
- `Pipeline.Run` drains an enqueue channel; `Enqueue` is non-blocking. Synchronous `Build` exists for tests and the admin "rebuild now" path.
- `internal/render/watcher.go` subscribes (via fsnotify) to `content/posts`, `content/drafts`, `media/orig`, `themes/`, and the active config file, then calls `pipeline.Enqueue` on relevant events. The watcher's `ready` chan closes once every subscription registers — tests wait on it before writing.
- Debounce lives in the pipeline (`pipeline.go`), implemented as a single-goroutine `select` with `time.After`. **Do not** introduce `time.AfterFunc + Reset` (documented race where a queued fire runs concurrently with a reschedule).
- Webmention promotion (verified or rejected) calls `pipeline.Enqueue` via `wmSvc.OnMentionsChanged`, so a freshly verified mention re-bakes the target post page.
- Drafts get rendered to `_drafts/<HMAC-SHA256(salt, draft.id)>/index.html` — unguessable salted slug, never linked from anywhere. The salt is per-install, stored in the DB.

`internal/site` is therefore deliberately thin: a chi sub-router that `http.FileServer`s `cfg.Paths.Public` plus the dynamic `POST /webmention` receive endpoint. There is no template execution or DB query on the public request path. Steady-state public reads are one syscall from the kernel page cache.

## Architecture: cross-cutting rules

**SSRF**: every fetch of an operator-supplied URL goes through `internal/safehttp.NewClient()`. It resolves DNS itself and pins the dial to the resolved IP, blocking loopback / RFC-1918 / link-local / multicast / unspecified at dial time. Use this client — never `http.DefaultClient` — for feed polls, webmention source/target fetches, oembed lookups. DNS rebinding mitigation is partial; documented in `safehttp.go`.

**Auth** (`internal/auth`): bcrypt cost 12; all user/session state in SQLite. Cookie `mizu_session`, SameSite=Lax, HttpOnly, 30-day TTL. Public endpoints under `/admin/api/`: `me`, `setup`, `login`, `logout`. Everything else gated by `auth.Middleware` via `r.Group`. **First-run claim window**: the wizard accepts the first account creation for `auth.SetupWindowDuration` (30 min) after boot with zero users — guardrail against a stranger racing the operator on the open internet. Failed-login attempts are tracked **by email** (so attempts against nonexistent accounts also rate-limit and don't leak existence by behavior).

**TLS / HTTPS** (`internal/server`): `TLSManager` wraps a `certmagic.Config` + cache and runs the `:80`/`:443` listeners on top of the always-running plain listener. Enabling TLS is a runtime operation: the wizard's "Enable HTTPS" handler flips it via the `TLSController` interface that `admin` holds. **CertMagic owns all retry/backoff/DNS-not-ready logic** — `main.go` just calls `Enable` once. `tls.*` is persisted to `config.yml` only on the `cert_obtained` event (`tlsMgr.OnEnabled(adminSrv.PersistTLSConfig)`), so a failed issuance can't leave `enabled=true` on disk and Fatalf the next restart. HSTS is one year, `includeSubdomains` — comment in `config.example.yml` flags the foot-gun.

**Trust boundary at the public listener**: mizu binds the public listener directly with no trusted reverse proxy in front, so the router **deliberately does not** use `middleware.RealIP`. Any `X-Forwarded-For` is attacker-controlled and would let a caller spoof IP to defeat per-IP rate limits. Don't add it back.

**Rate / body limits** (`internal/server`): per-IP rate limits (login, setup, webmention, global safety-net) and per-route body-size caps live under `limits:` in config. Defaults are tuned for a single-user appliance on the open internet — comments in `config.example.yml` document the intent.

**Single-binary deploy**: `admin/dist/` (built React) and `themes/` are embedded via `go:embed` in `embed.go` at the project root (embed paths are relative to source file). `cfg.Paths.AdminDist` overrides the admin SPA dir on disk when present. Public-site themes resolve through `internal/theme.Load`: the embedded `themes/default/` is the fallback floor, and any theme name resolves first against `./themes/<name>/` on disk via a layered FS — custom themes ship just the files they want to override. Selection lives under the top-level `theme:` block (`name`, `settings`).

## Architecture: package map

- `main.go` — wires everything; every background worker registers with `bg.Add(1)` / `defer bg.Done()` so shutdown drains cleanly.
- `embed.go` — `//go:embed all:admin/dist` and `//go:embed all:themes`. The `all:` prefix is required so hashed asset files starting with `_`/`.` aren't silently dropped.
- `internal/post` — `Store` (posts + drafts). `Reload()` holds the write lock for the full read-and-swap so a concurrent Create can't be silently dropped by the index swap. No watcher in this package anymore — see `internal/render/watcher.go`.
- `internal/render` — the bake pipeline, stages, snapshot, template cache, content hashing, and the fsnotify watcher.
- `internal/site` — public mux: file server over `cfg.Paths.Public/` plus `POST /webmention`. Adds `Link: </webmention>; rel="webmention"` and `X-Content-Type-Options: nosniff` where appropriate. A `ConfiguredFn` cache flips the public root to a friendly placeholder until the wizard finishes (one-way false→true, cached so the DB isn't hit per request).
- `internal/admin` — REST API and SPA serving. Routes under `/admin/api/*`; SPA fallback under `/admin/*` serves the embedded React shell for any unknown path so client-side routing takes over.
- `internal/auth` — DB-backed users, sessions, lockout, session reaper.
- `internal/db` — the one SQLite file, writer/reader pool split, embedded migrations applied via `PRAGMA user_version`.
- `internal/feeds` — subscriptions, OPML I/O, fetched-item storage, polling worker (conditional GET via etag/last-modified, uses `safehttp`), timeline read API. Item content is sanitized at ingest with `bluemonday.UGCPolicy()` so React can `dangerouslySetInnerHTML` without re-sanitizing.
- `internal/webmention` — receive endpoint + verifier worker + outbound sender. **Pending mentions are re-enqueued from the DB on startup**; transient fetch errors leave the row at `StatusPending` for next-process retry. Only `ErrLinkNotFound` (definite "source doesn't link to us") flips to `StatusRejected`. `OnMentionsChanged` callback fires whenever a mention reaches a terminal state, used by main.go to kick the render pipeline.
- `internal/media` — image upload + resize. Original kept verbatim at `media/orig/<name>`; display variant baked by `ImageVariantStage` to `public/media/<name>`, capped at 1600px long edge. Type detection is by magic bytes (`http.DetectContentType`), never the client-supplied Content-Type. PNG→PNG passthrough when small, JPEG→JPEG q=85, WebP→JPEG (stdlib can't encode WebP), GIF passthrough. SVG intentionally rejected (XSS surface).
- `internal/server` — TLS manager (CertMagic), `SecureHeaders` middleware, per-IP `RateLimit` middleware.
- `internal/safehttp` — SSRF-safe HTTP client. Used by `feeds` and `webmention`.
- `internal/theme` — layered FS that overlays disk theme on embedded default; theme settings exposed to stages via the Snapshot.
- `internal/netinfo` — public-IP cache the admin "DNS preflight" step reads.
- `internal/config` — YAML loader, creates writable directories on startup.

## Build gotchas

- `//go:embed all:admin/dist` requires the directory to exist with at least one file. A committed `admin/dist/.keep` covers fresh checkouts; `npm run build` ends with `touch dist/.keep` because Vite's `emptyOutDir: true` clears the directory on every build. Both `.gitignore` files have negation rules to keep `.keep` tracked. **Do not flip `emptyOutDir` to false** — stale assets would accumulate.
- `go build` from a cold clone before `npm run build` will fail with an embed error. Run `make build`, or `cd admin && npm install && npm run build` once.
- `modernc.org/sqlite` is pure Go. The Dockerfile builds with `CGO_ENABLED=0` and ships a fully static binary on Alpine.

## Testing

**Go tests** live next to source as `_test.go`. Conventions:

- `t.TempDir()` for every store/file path. Real SQLite, real disk, real chi router — internal collaborators are not mocked. Refactoring internals shouldn't break tests.
- HTTP-touching services accept dependency injection so loopback `httptest` servers don't trip `safehttp`. `webmention.Service.http` and `feeds.Poller.http` are field-swappable; tests assign `http.DefaultClient`. `feeds.Service.validate` is a swappable validator field with the same intent.
- **Cross-package test seam pattern**: `internal/feeds/testhelpers.go` exposes `Service.SetValidateForTest` and `SetPollerHTTPForTest`. Intentionally NOT `_test.go` — `_test.go` files aren't visible from other packages, and the `admin` tests need to drive `feeds` through its public surface. Keep this surface minimal; don't grow it to expose unrelated internals.
- The webmention package follows the same pattern via direct `s.http = http.DefaultClient` assignment in `webmention_test.go:newService`.
- `internal/media/extra_test.go` carries an inlined 64-byte WebP fixture (`minimalWebP`). Stdlib has no WebP encoder, so the bytes were generated once via `cwebp`. The test self-verifies via `http.DetectContentType` so a corrupt fixture fails loudly.
- The render watcher exposes a `ready` channel so tests can wait for fsnotify subscriptions to register before writing files. Without it, the first write races the watcher's `Add()` calls.

**TS tests** live next to source as `*.test.ts(x)`. Conventions:

- Vitest + React Testing Library + jsdom. Setup in `admin/src/test/setup.ts` (jest-dom matchers + RTL `cleanup` after each test).
- **`queueFetch` helper** (`admin/src/test/fetch.ts`) is the canonical way to drive components: pass an array of `{status, body?}` replies, get back a `vi.fn()`. It stubs `globalThis.fetch`, auto-cleans via its own `afterEach(() => vi.unstubAllGlobals())`, throws on overrun (missing-stub bugs fail loudly instead of hanging), and handles the spec quirk that `Response` rejects empty-string bodies on 204.
- Tests assert against visible text and ARIA roles, not DOM structure or test ids. `dangerouslySetInnerHTML` paths are verified by querying rendered children (`container.querySelector(".post-rendered em")`) — don't re-implement the sanitizer in test fixtures.
- **Lexical-in-jsdom limitation**: `MarkdownEditor` tests only cover mount, placeholder, hydration from initial markdown, and the imperative handle. Lexical relies on `contenteditable` + `Selection` APIs jsdom only partially implements, so interactive typing isn't unit-tested. The comment in `MarkdownEditor.test.tsx` documents the gap; cover those paths via browser smoke testing.
- Stub `globalThis.confirm` directly when a flow calls it (Drafts/Subscriptions destructive actions). Pair with an `afterEach(() => vi.unstubAllGlobals())` in the same describe — `queueFetch` only owns its own cleanup.

## Style + workflow

- Code-review subagents run from a `/review` skill. Run after any non-trivial slice; security-reviewer for anything that touches auth, untrusted input, or HTTP fetches.
- Frontmatter regex (`internal/post/post.go`) deliberately consumes up to two newlines after the closing `---` — exactly the convention's separator — and preserves any further leading blanks as authored content. There's a regression test; don't relax it back to greedy.
- React composer state lives in `HomeView`. Cross-tab handoffs (e.g., Drafts → composer) go via `Shell.editTarget`, consumed once via `useEffect` and cleared with `onEndConsumed`.
- HTML-rendered post bodies use `dangerouslySetInnerHTML` for sanitized feed content. The trust boundary is `bluemonday` at ingest, not at render.
- When adding a new render stage, follow the `Stage` interface in `internal/render/stage.go` and register it in `DefaultStages()`. Respect the caching invariant: if your stage caches anything, the cache key must hash every input the value depends on.
