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
#   executable  (default) runtime image, runs as non-root uid 65532
#
# Base images are pinned by digest for reproducibility; bump the tag and the
# digest together (Dependabot keeps both updated).

# Toolchain pinned to the Go version required by go.mod.
FROM golang:1.25-alpine3.23@sha256:60e626bbde32def8694687d03536ea4341b19e5f068e9a630225a1dfbd0505c9 AS common

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

COPY *.go ./

# The go-build cache mount persists compiled objects across builds (locally
# via BuildKit state, in CI via buildkit-cache-dance), so incremental
# rebuilds only recompile changed packages.
FROM common AS test
RUN --mount=type=cache,target=/root/.cache/go-build \
    go test -trimpath -ldflags="-s -w"

FROM common AS dev
CMD ["go", "run", "."]

FROM common AS build
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w"

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 AS executable

LABEL org.opencontainers.image.title="pacoloco" \
      org.opencontainers.image.description="Caching proxy server for Arch Linux pacman repositories" \
      org.opencontainers.image.source="https://github.com/anatol/pacoloco" \
      org.opencontainers.image.licenses="MIT"

# tzdata: TZ support for prefetch cron schedules and log timestamps.
# The image runs as the dedicated non-root user 65532 ("nonroot" convention);
# the cache directory is pre-created with matching ownership so the container
# works out of the box, with or without a mounted volume.
# hadolint ignore=DL3018
RUN apk add --no-cache tzdata \
 && addgroup -g 65532 pacoloco \
 && adduser -D -H -u 65532 -G pacoloco -s /sbin/nologin pacoloco \
 && install -d -o pacoloco -g pacoloco /var/cache/pacoloco

WORKDIR /pacoloco

COPY --from=build /build/pacoloco .

USER 65532:65532

EXPOSE 9129

CMD ["/pacoloco/pacoloco"]
