# syntax=docker/dockerfile:1

# Multi-stage build for pacoloco.
#
# Targets:
#   test        run the Go test suite (used by CI, per target platform)
#   build       compile the release binary
#   dev         full toolchain for development; mount the source and the Go
#               caches to iterate quickly:
#                 docker build --target dev -t pacoloco:dev .
#                 docker run --rm -it \
#                   -v "$PWD":/build \
#                   -v pacoloco-gomod:/go/pkg/mod \
#                   -v pacoloco-gobuild:/root/.cache/go-build \
#                   -v "$PWD"/pacoloco.yaml:/etc/pacoloco.yaml:ro \
#                   -p 9129:9129 pacoloco:dev
#   executable  (default) runtime image, runs as non-root APP_UID
#
# Base images are pinned by digest for reproducibility; bump the tag and the
# digest together (Dependabot keeps both updated).

# Shared build-time knobs. Global ARGs only carry the defaults: a stage
# that needs one re-imports it with a bare `ARG <name>`.
#
# Dedicated non-root runtime identity; 65532 is the de-facto "nonroot"
# convention (matches distroless).
ARG APP_UID=65532
ARG APP_GID=65532
ARG APP_USER=pacoloco
# Go build cache location; CI (the buildkit-cache-dance cache-map) and
# the dev volume above must mount this same path.
ARG GOCACHE=/root/.cache/go-build

# Toolchain pinned to the Go version required by go.mod.
FROM golang:1.26-alpine3.23@sha256:18b460dd17542c2ba43299a633cf6ebfc1115101509531471d7cfce1019af083 AS common

ARG GOCACHE
# GOFLAGS applies to every go test/build/run below; GOCACHE pins the
# cache-mount location explicitly instead of relying on $HOME.
ENV GOFLAGS=-trimpath \
    GOCACHE=$GOCACHE

# gcc/libc-dev: cgo toolchain for the sqlite driver. Package versions ride
# the pinned base image digest instead of apk pins, which Alpine mirrors
# break by dropping old package revisions.
# hadolint ignore=DL3018
RUN apk add --no-cache gcc libc-dev

WORKDIR /build

# Modules in their own layer: source edits don't invalidate the download,
# and the layer is restorable from registry/GHA layer caches.
COPY go.mod go.sum ./
RUN go mod download

COPY . ./

# The go-build cache mount persists compiled objects across builds (locally
# via BuildKit state, in CI via buildkit-cache-dance), so incremental
# rebuilds only recompile changed packages.
FROM common AS test
RUN --mount=type=cache,target=$GOCACHE \
    go test ./...

FROM common AS dev
CMD ["go", "run", "."]

FROM common AS build
# -ldflags only affects linking, so build shares the compile cache with
# the test stage above despite the extra flag.
RUN --mount=type=cache,target=$GOCACHE \
    go build -ldflags="-s -w" -o pacoloco .

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS executable

ARG APP_UID
ARG APP_GID
ARG APP_USER

LABEL org.opencontainers.image.title="pacoloco" \
      org.opencontainers.image.description="Caching proxy server for Arch Linux pacman repositories" \
      org.opencontainers.image.source="https://github.com/anatol/pacoloco" \
      org.opencontainers.image.licenses="MIT"

# tzdata: TZ support for prefetch cron schedules and log timestamps.
# The cache directory is pre-created with matching ownership so the
# container works out of the box, with or without a mounted volume.
# hadolint ignore=DL3018
RUN apk add --no-cache tzdata \
 && addgroup -g "$APP_GID" "$APP_USER" \
 && adduser -D -H -u "$APP_UID" -G "$APP_USER" -s /sbin/nologin "$APP_USER" \
 && install -d -o "$APP_USER" -g "$APP_USER" "/var/cache/$APP_USER"

WORKDIR /pacoloco

COPY --from=build /build/pacoloco .

USER $APP_UID:$APP_GID

EXPOSE 9129

# Exec-form CMD cannot expand variables, so the path stays literal.
CMD ["/pacoloco/pacoloco"]
