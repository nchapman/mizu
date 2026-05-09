# repeat

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
cp config.example.yml config.yml
# edit config.yml — at minimum set site.base_url to where this will be served
make build
./repeat --config config.yml
```

On first run the server prints a one-time setup token. Visit
`http://localhost:8080/admin` and paste it to set your password.

## Development

```sh
# Backend on :8080 with the embedded admin
./repeat --config config.yml

# Admin with hot reload on :5173, proxying /admin/api → :8080
cd admin && npm install && npm run dev
```

`make check` runs gofmt, `go vet`, staticcheck, ESLint, `tsc --noEmit`,
the test suite, and a full build. Run it before committing.

## Docker

```sh
docker build -t repeat .
docker run -d \
  -p 8080:8080 \
  -v $PWD/config.yml:/app/config.yml:ro \
  -v $PWD/data/content:/app/content \
  -v $PWD/data/media:/app/media \
  -v $PWD/data/cache:/app/cache \
  -v $PWD/data/state:/app/state \
  repeat
```

The image runs as a non-root user. Mount `config.yml` and the four data
directories as volumes so user data survives container restarts.

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
