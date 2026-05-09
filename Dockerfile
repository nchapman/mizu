# syntax=docker/dockerfile:1

# Multi-stage build. The final image is a small Alpine runtime with a
# single Go binary; the React admin is embedded into the binary at
# build time, so there's nothing else to ship.

# --- Stage 1: build the React admin ----------------------------------
FROM node:20-alpine AS admin
WORKDIR /src/admin

# Lockfile-only install for layer caching: this layer only invalidates
# when dependencies change, not on every source edit.
COPY admin/package.json admin/package-lock.json ./
RUN npm ci

COPY admin/ ./
RUN npm run build

# --- Stage 2: build the Go binary ------------------------------------
FROM golang:1.25-alpine AS go
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# admin/dist is the embed source; bring it in from the previous stage.
COPY --from=admin /src/admin/dist ./admin/dist

# CGO_ENABLED=0 because modernc.org/sqlite is pure Go and we want a
# fully static binary that doesn't depend on libc.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/repeat .

# --- Stage 3: runtime ------------------------------------------------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -S repeat && adduser -S -G repeat -h /app repeat
WORKDIR /app

COPY --from=go /out/repeat /app/repeat
COPY config.example.yml /app/config.example.yml

# Pre-create the writable directories with the right ownership so
# bind/anonymous mounts inherit it. Done before the USER switch
# because chown after dropping root would fail.
RUN mkdir -p /app/content/posts /app/content/drafts \
        /app/media /app/cache /app/state && \
    chown -R repeat:repeat /app

USER repeat

# Mount these as volumes so user data survives container restarts.
VOLUME ["/app/content", "/app/media", "/app/cache", "/app/state"]
EXPOSE 8080

# Liveness check: the public site root is served from the same
# binary, so a 200 here means the process is up and the site server
# is responding. Avoids a hung-process going unnoticed.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
    CMD wget -qO- http://localhost:8080/ >/dev/null || exit 1

# The container expects a config file at /app/config.yml — the
# operator can either mount one or copy the example and edit it.
ENTRYPOINT ["/app/repeat", "--config", "/app/config.yml"]
