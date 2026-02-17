# Configuration Reference

Pacoloco is configured via a YAML file located at `/etc/pacoloco.yaml`.

## General Settings

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `cache_dir` | string | `/var/cache/pacoloco` | Cache directory path. Must be readable and writable. |
| `address` | string | `""` (all interfaces) | Server listen address. |
| `port` | int | `9129` | Server listen port. |
| `purge_files_after` | int | `0` (disabled) | Seconds of inactivity before purging cached files. Minimum 600 (10 minutes) if enabled. |
| `download_timeout` | int | `0` (no timeout) | Timeout in seconds for upstream downloads. |
| `http_proxy` | string | `""` | Global HTTP proxy URL for upstream requests. |
| `user_agent` | string | `"Pacoloco/1.2"` | User-Agent header for upstream requests. |
| `set_timestamp_to_logs` | bool | `false` | Add timestamps to log output. |

## Repository Configuration (`repos`)

Each repository is a named entry under the `repos` key. Exactly one URL source must be specified: `url`, `urls`, or `mirrorlist`.

| Option | Type | Description |
|--------|------|-------------|
| `url` | string | Single upstream mirror URL. |
| `urls` | []string | Multiple upstream mirror URLs (tried in order for failover). |
| `mirrorlist` | string | Path to a pacman-style mirrorlist file. File must exist and be readable. |
| `http_proxy` | string | Per-repo HTTP proxy, overrides global `http_proxy`. |

### Validation Rules

- `url` and `urls` are mutually exclusive.
- `url` and `mirrorlist` are mutually exclusive.
- `urls` and `mirrorlist` are mutually exclusive.
- At least one URL source is required for every repo.

## Prefetch Configuration (`prefetch`)

Optional section. When present, enables package prefetching.

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `cron` | string | required | Cron expression for prefetch schedule (7-field format: `sec min hour dom month dow year`). |
| `ttl_unaccessed_in_days` | int | `30` | Days after which unaccessed packages stop being prefetched. |
| `ttl_unupdated_in_days` | int | `200` | Days after which packages not updated upstream are removed. |

### Validation

- Both TTL values must be positive.
- The `cron` expression must be valid per the [gorhill/cronexpr](https://github.com/gorhill/cronexpr#implementation) specification.

## TLS Configuration (`tls`)

Optional section. Both fields are required when the section is present.

| Option | Type | Description |
|--------|------|-------------|
| `cert` | string | Path to TLS certificate file (PEM). Must be readable. |
| `key` | string | Path to TLS private key file (PEM). Must be readable. |

## Complete Example

```yaml
cache_dir: /var/cache/pacoloco
address: 0.0.0.0
port: 9129
purge_files_after: 2592000  # 30 days
download_timeout: 3600      # 1 hour
http_proxy: http://proxy.example.com:8080
user_agent: "Pacoloco/1.2"
set_timestamp_to_logs: true

repos:
  archlinux:
    urls:
      - http://mirror.rackspace.com/archlinux
      - https://mirror.leaseweb.net/archlinux

  custom-repo:
    url: https://my-custom-mirror.example.com/repo

  mirrorlist-repo:
    mirrorlist: /etc/pacman.d/mirrorlist
    http_proxy: http://special-proxy.example.com:3128

prefetch:
  cron: "0 0 3 * * * *"       # every day at 3:00 AM
  ttl_unaccessed_in_days: 30
  ttl_unupdated_in_days: 200

tls:
  cert: /etc/pacoloco/cert.pem
  key: /etc/pacoloco/key.pem
```

## Minimal Example

```yaml
cache_dir: /var/cache/pacoloco

repos:
  archlinux:
    url: http://mirror.rackspace.com/archlinux
```
