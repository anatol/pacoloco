# Pacoloco Architecture

## 1. Overview

Pacoloco is a single-binary caching proxy server for Arch Linux pacman repositories written in Go. It sits between pacman clients and upstream mirror servers, downloading packages on demand and caching them locally on the filesystem. Once a package is cached, subsequent requests from any client are served directly from the local cache without contacting the upstream mirror.

Key capabilities:

- **On-demand caching**: Packages are fetched from upstream only when first requested, then served from cache for all future requests.
- **Prefetching**: A cron-based engine proactively downloads updated packages before clients request them, reducing latency for common updates.
- **Multiple mirror support**: Each repository can be backed by a single URL, a list of URLs, or a mirrorlist file. Pacoloco tries mirrors in order until one succeeds.
- **Cache purging**: Stale cached files are automatically removed based on configurable access-time thresholds.
- **Prometheus metrics**: Built-in monitoring endpoint exposes cache hit/miss rates, error counts, and storage statistics.

## 2. Component Diagram

```
                          +---------------------------+
                          |      Upstream Mirrors     |
                          |  (Arch Linux repo servers)|
                          +---------------------------+
                                     ^
                                     | HTTP(S) requests
                                     |
+----------------+          +--------+------------------+
|                |          |        pacoloco           |
| pacman client  +--------->+                           |
|                |  HTTP    |  +---------------------+  |
+----------------+          |  | HTTP Server          |  |
                            |  | (pacoloco.go)        |  |
+----------------+          |  |                      |  |
|                |          |  |  /repo/* -> proxy    |  |
| pacman client  +--------->+  |  /metrics -> prom   |  |
|                |  HTTP    |  +---------------------+  |
+----------------+          |           |               |
                            |           v               |
+----------------+          |  +---------------------+  |
|                |          |  | Downloader           |  |
| pacman client  +--------->+  | (downloader.go)      |  |
|                |  HTTP    |  | sync.Cond streaming  |  |
+----------------+          |  +---------------------+  |
                            |       |           |       |
                            |       v           v       |
                            |  +--------+  +--------+  |
                            |  | Cache   |  | SQLite |  |
                            |  | (fs)    |  | (GORM) |  |
                            |  | pkgs/   |  | prefetch| |
                            |  +--------+  +--------+  |
                            +---------------------------+
```

## 3. Project Structure

| File | Purpose |
|---|---|
| `pacoloco.go` | Entry point, HTTP handler and request routing, Prometheus metrics definitions and registration |
| `config.go` | YAML configuration parsing, default values, and validation logic |
| `downloader.go` | Concurrent file downloading with `sync.Cond` synchronization, streaming responses via `DownloadReader` |
| `urls.go` | URL resolution from single `url` field, `urls` array, or `mirrorlist` file paths |
| `prefetch.go` | Cron-based prefetch engine that updates cached packages proactively |
| `prefetch_db.go` | SQLite database schema and operations via GORM (packages, mirror_dbs, mirror_packages tables) |
| `repo_db_mirror.go` | Tar extraction from mirror `.db` files, mirror package metadata building |
| `uncompress.go` | Decompression support (gzip, xz, zstd) with magic byte detection and 100MB bomb protection limit |
| `purge.go` | Stale file purge based on file access time |
| `utils.go` | Shared utility functions |

## 4. HTTP Server and Routing

Pacoloco exposes two HTTP route families:

- **`/repo/`** -- The main proxy route that handles all pacman repository requests.
- **`/metrics`** -- Prometheus metrics endpoint.

The proxy route uses the following URL regex to decompose incoming requests:

```
^/repo/([^/]*)(/.*)?/([^/]*)$
```

This extracts three components:

| Capture Group | Name | Example |
|---|---|---|
| 1 | `repoName` | `archlinux` |
| 2 | `pathAtRepo` | `/community/os/x86_64` |
| 3 | `fileName` | `vim-9.0.1-1-x86_64.pkg.tar.zst` |

The `repoName` is looked up in the YAML configuration to find the corresponding upstream mirror URLs. The `pathAtRepo` and `fileName` are combined to form the upstream request path and the local cache path.

## 5. File Classification

Pacoloco classifies requested files into two categories that determine caching behavior:

**Mutable files** -- These are re-checked against the upstream server on every request:
- `.db` (repository database)
- `.db.sig` (repository database signature)
- `.files` (file listing database)

These files change frequently as packages are added or updated in the repository. Pacoloco uses `If-Modified-Since` headers to avoid re-downloading unchanged content.

**Immutable files** -- These are served directly from cache if present, with no upstream check:
- `.pkg.tar.*` (package archives)

Package files are content-addressed by their version in the filename, so once cached they never need to be re-fetched.

## 6. Downloader

The downloader subsystem (`downloader.go`) is the core data path. It uses `sync.Cond` to allow multiple concurrent readers to stream from a single in-progress download.

### Key Flow

1. **`getDownloader()`** -- Looks up or creates a `Downloader` for the requested file path. If a download is already in progress for the same file, the caller joins the existing download rather than starting a new one.

2. **Async goroutine** -- Each `Downloader` spawns a background goroutine that performs the actual download. This goroutine writes data to the cache file and signals waiting readers via `sync.Cond.Broadcast()`.

3. **`download()`** -- Iterates through each configured mirror URL for the repository, attempting to download the file. Falls through to the next mirror on failure.

4. **`downloadFromUpstream()`** -- Performs the HTTP request to a specific upstream mirror. Handles:
   - `If-Modified-Since` conditional requests for mutable files
   - `Content-Length` validation to detect truncated downloads
   - Proper cleanup on failure

5. **`DownloadReader`** -- Implements `io.ReadSeekCloser` to provide a streaming interface. Multiple HTTP response writers can read from the same download concurrently, each tracking their own read position while the background goroutine writes ahead.

6. **Cleanup** -- An atomic `usageCount` tracks the number of active readers. When the last reader closes, the `Downloader` is removed from the active downloads map.

## 7. Configuration

Configuration is loaded from a YAML file with the following structure and defaults:

| Field | Default | Description |
|---|---|---|
| `port` | `9129` | HTTP listen port |
| `cache_dir` | `/var/cache/pacoloco` | Local cache directory |
| `purge_files_after` | (disabled) | Duration after which unused files are purged |
| `download_timeout` | (none) | HTTP timeout for upstream requests |
| `repos` | (required) | Map of repository configurations |
| `prefetch` | (disabled) | Prefetch cron schedule |
| `tls_cert` / `tls_key` | (disabled) | TLS certificate and key paths |

### Validation Rules

The configuration parser validates the following constraints and returns errors rather than calling `log.Fatal`:

- **Mutual exclusivity**: Each repo must specify exactly one of `url`, `urls`, or `mirrorlist` -- never more than one.
- **Cache directory**: Must be writable by the running process.
- **Purge interval**: If set, `purge_files_after` must be at least 10 minutes to prevent excessive filesystem operations.
- **TLS files**: If TLS is configured, both `tls_cert` and `tls_key` must be specified and readable.
- **Cron expression**: If prefetch is enabled, the cron expression must be valid.
- **TTL**: If set, must be a positive duration.

## 8. Prefetch Engine

The prefetch engine (`prefetch.go`) proactively downloads updated packages so they are already cached when clients request them. It is scheduled via cron expressions parsed by `gorhill/cronexpr`.

### Lifecycle

1. **`cleanPrefetchDB()`** -- Purges stale entries from the SQLite database that are no longer relevant (e.g., packages not downloaded recently).

2. **`updateMirrorsDbs()`** -- Downloads the latest `.db` files from each configured upstream mirror and parses them to extract current package metadata. This populates the `mirror_packages` table.

3. **`getPkgsToUpdate()`** -- Joins the `packages` table (tracking what clients have requested) with the `mirror_packages` table (tracking what upstream offers) to identify packages where the upstream version is newer than the cached version.

4. **`prefetchRequest()`** -- Downloads each identified updated package, placing it in the cache so it is ready for the next client request.

## 9. Database Schema

The prefetch system uses SQLite via GORM with three tables, all using composite primary keys:

### `packages`

Tracks packages that clients have requested through the proxy.

| Column | Type | Key | Description |
|---|---|---|---|
| `package_name` | string | PK | Package name (e.g., `vim`) |
| `arch` | string | PK | Architecture (e.g., `x86_64`) |
| `repo_name` | string | PK | Repository name from config |
| `version` | string | | Package version string |
| `last_time_downloaded` | time | | When a client last requested this package |
| `last_time_repo_updated` | time | | When the repo DB was last updated |

### `mirror_dbs`

Tracks upstream mirror database files that have been downloaded.

| Column | Type | Key | Description |
|---|---|---|---|
| `url` | string | PK | Mirror base URL |
| `repo_name` | string | PK | Repository name |
| `last_time_downloaded` | time | | When the .db file was last fetched |

### `mirror_packages`

Tracks the current package versions available on upstream mirrors.

| Column | Type | Key | Description |
|---|---|---|---|
| `package_name` | string | PK | Package name |
| `arch` | string | PK | Architecture |
| `repo_name` | string | PK | Repository name |
| `version` | string | | Upstream version string |
| `file_ext` | string | | File extension (e.g., `.pkg.tar.zst`) |
| `download_url` | string | | Full URL for downloading |

## 10. Mirror DB Parsing

The mirror database parsing pipeline (`repo_db_mirror.go`) extracts package metadata from upstream `.db` files:

1. **Download** -- Fetch the `.db` file from the upstream mirror.
2. **Decompress** -- Pass through `uncompress.go` which detects the compression format via magic bytes (gzip, xz, or zstd) and decompresses accordingly. A 100MB decompression bomb limit is enforced.
3. **Tar extraction** -- Iterate through tar entries, selecting only those matching the pattern `*/desc` (package description files).
4. **Parse** -- Extract the `%FILENAME%` field from each `desc` entry using regex matching. This filename contains the package name, version, architecture, and file extension needed to populate the `mirror_packages` table.

## 11. Cache Purge

The cache purge system (`purge.go`) runs on a daily ticker:

1. **Walk** -- Traverses the `pkgs/{repoName}/` directory tree within the cache directory.
2. **Access time check** -- For each file, reads the access time using `djherbis/times` and compares it against the configured `purge_files_after` threshold.
3. **Remove** -- Deletes files whose access time is older than the threshold.
4. **Metrics update** -- Updates Prometheus gauges for cache size (bytes) and package count per repository after purging.

## 12. URL Management

Each repository in the configuration can specify upstream mirrors in one of three ways (mutually exclusive):

- **`url`** -- A single upstream mirror URL.
- **`urls`** -- An ordered array of upstream mirror URLs. Pacoloco tries them sequentially until one succeeds.
- **`mirrorlist`** -- Path to a mirrorlist file (same format as `/etc/pacman.d/mirrorlist`).

### Mirrorlist Handling

Mirrorlist files are parsed using regex to extract `Server = ...` lines. The parsed result is cached in memory with a **5-second check interval**: on each access, the file's modification time is compared against the last known value using a mutex for thread safety. If the file has been modified, it is re-parsed. This allows administrators to update the mirrorlist without restarting pacoloco.

## 13. Prometheus Metrics

Pacoloco exposes 7 Prometheus metrics at the `/metrics` endpoint:

| Metric | Type | Labels | Description |
|---|---|---|---|
| `pacoloco_cache_requests_total` | Counter | `repo` | Total number of proxy requests |
| `pacoloco_cache_hits_total` | Counter | `repo` | Requests served from cache |
| `pacoloco_cache_miss_total` | Counter | `repo` | Requests that required upstream fetch |
| `pacoloco_cache_errors_total` | Counter | `repo` | Failed requests (upstream and local errors) |
| `pacoloco_cache_size_bytes` | Gauge | `repo` | Total size of cached files per repository |
| `pacoloco_cache_packages_total` | Gauge | `repo` | Number of cached package files per repository |
| `pacoloco_downloaded_files_total` | Counter | `repo`, `upstream`, `status` | Files downloaded from upstream, labeled by mirror and HTTP status |

## 14. Deployment

Pacoloco supports multiple deployment methods:

### Docker

A multi-stage Docker build is used:

1. **Build stage** -- Uses `golang:alpine` as the base image to compile the Go binary.
2. **Runtime stage** -- Uses a minimal `alpine` image containing only the compiled binary and necessary runtime dependencies.

The Docker image exposes port 9129 and expects the configuration file and cache directory to be mounted as volumes.

### Systemd

A systemd service unit is provided for running pacoloco as a system daemon. The service manages the lifecycle of the single binary process.

### Arch Linux Package

Pacoloco is available as an official Arch Linux package, installable directly via pacman.
