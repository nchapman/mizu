# mizu

A self-hosted, single-user microblog and feed reader. One binary publishes
your writing as HTML + RSS, and reads the feeds you subscribe to. Written
in Go with a small React admin.

Features:

- Notes and articles, written in markdown via a web composer or your editor
- Drafts, image upload (with auto-resize), edit/delete
- RSS / Atom feed reader with a unified timeline
- Webmentions, both directions
- Single static binary, no external services required

## Quick start

Requires Go 1.25+ and Node 20+.

```sh
make build
./mizu
```

That's it. The first time you boot with no `config.yml` on disk, mizu runs in
fresh-install mode: open `http://<this-host>:8080/admin` in a browser and the
setup wizard walks you through account creation, site basics, an optional DNS
preflight, and optional Let's Encrypt issuance. The wizard writes `config.yml`
on its way through.

**First-run claim window.** The wizard accepts the first account creation for
30 minutes after the binary first boots with zero users. After that the wizard
refuses to claim the instance — a guardrail against a stranger on the open
internet racing you to setup. If you miss the window, stop the binary, delete
`state/mizu.db`, and restart; posts, drafts, and media live on disk and are
unaffected.

For advanced deployments — hand-tuned ports, paths, rate limits — start from
`config.example.yml` and pass `--config`.

## Development

```sh
# Backend on :8080 with the embedded admin
./mizu --config config.yml

# Admin with hot reload on :5173, proxying /admin/api → :8080
cd admin && npm install && npm run dev
```

`make check` runs gofmt, `go vet`, staticcheck, ESLint, `tsc --noEmit`,
the test suite, and a full build. Run it before committing.

## Docker

```sh
docker build -t mizu .
docker run -d \
  -p 8080:8080 \
  -v $PWD/data/content:/app/content \
  -v $PWD/data/media:/app/media \
  -v $PWD/data/cache:/app/cache \
  -v $PWD/data/state:/app/state \
  mizu
```

The image runs as a non-root user. The four data directories as volumes
so user data survives container restarts; the wizard writes `config.yml`
into `state` on its way through.

## VPS deploy with cloud-init + Watchtower

For a turnkey single-host deployment, `deploy/cloud-init.yaml` provisions
a fresh VPS end-to-end: it installs Docker, drops in
`deploy/docker-compose.yml`, and starts mizu alongside Watchtower (which
polls GHCR hourly and auto-updates on new stable tags).

Paste the contents of `deploy/cloud-init.yaml` as the user-data on any
cloud-init-supporting VPS (Hetzner, DigitalOcean, Oracle ARM, Lightsail,
etc.). Once it boots, point your domain's A record at the VPS IP and
open `http://<that-ip>:8080/admin` — the setup wizard handles the rest.

## Configuration

See `config.example.yml`. The defaults match the directory layout above;
the most common fields you'll change are `site.title`, `site.base_url`,
`site.author`, and `server.addr`.

The admin SPA and HTML templates are embedded into the binary at build
time. To override either with on-disk versions (for theming or local
iteration without a rebuild), set `paths.admin_dist` or `paths.templates`
to a directory and rebuild your assets there.

## Storage layout

Your content lives on disk as plain files — no database for the things
you write:

- `content/posts/*.md` — published posts (frontmatter + markdown)
- `content/drafts/*.md` — unpublished drafts
- `media/orig/*` — uploaded images, original
- `media/*` — display variants, capped at 1600px long edge
- `subscriptions.opml` — feed subscription list
- `state/auth.json` — password hash and session secrets
- `state/webmentions.log.jsonl` — durable webmention archive

SQLite databases under `cache/` (feed items, read state, webmention
index) are regeneratable from the durable sources above and can be
deleted at any time.

## License

MIT.
