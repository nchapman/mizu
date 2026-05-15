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
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mizu .

# --- Stage 3: runtime ------------------------------------------------
FROM alpine:3.20

# OCI labels: `image.source` lets GHCR auto-link the package to the
# repo. The watchtower label opts this image in to label-scoped
# auto-updates so a Watchtower sidecar started with --label-enable
# only touches mizu, not other containers on the host.
LABEL org.opencontainers.image.source="https://github.com/nchapman/mizu" \
      org.opencontainers.image.title="mizu" \
      org.opencontainers.image.description="Self-hosted single-user microblog and feed reader" \
      org.opencontainers.image.licenses="MIT" \
      com.centurylinklabs.watchtower.enable="true"

RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -S mizu && adduser -S -G mizu -h /app mizu
WORKDIR /app

COPY --from=go /out/mizu /app/mizu
COPY config.example.yml /app/config.example.yml

# Pre-create the writable directories with the right ownership so
# bind/anonymous mounts inherit it. Done before the USER switch
# because chown after dropping root would fail.
RUN mkdir -p /app/content/posts /app/content/drafts \
        /app/media /app/cache /app/state && \
    chown -R mizu:mizu /app

USER mizu

# Mount these as volumes so user data survives container restarts.
VOLUME ["/app/content", "/app/media", "/app/cache", "/app/state"]

# 8080 is the always-on plain-HTTP listener (host maps :80 here).
# 8443 is bound by the in-process TLS manager once HTTPS is enabled
# (host maps :443 here). Both internal ports are above 1024 so the
# container runs unprivileged. Compose files publish them as needed.
EXPOSE 8080 8443

# The container expects a config file at /app/config.yml — the
# operator can either mount one or copy the example and edit it.
ENTRYPOINT ["/app/mizu", "--config", "/app/config.yml"]
