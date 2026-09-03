---
title: Configuration
description: Every variable Falco reads, where it comes from, and the four that have no default on purpose.
---

Configuration has three layers, and the last one wins:

1. **Defaults compiled into the binary** (`internal/config/defaults.go`)
2. **`config.yaml`** in the working directory
3. **Environment variables**

A `.env` file in the working directory is loaded too, but it never overrides a
variable that is already set in the environment.

:::caution[`config.yaml` is read from the working directory]
Running the binary from the source tree picks up the repo's `config.yaml`, which
declares buckets you may not have credentials for. That is deliberate for
development and surprising everywhere else — run it from an empty directory, or
from the container, to get a clean configuration.
:::

## The four with no default

Falco refuses to invent these. Absent, they either stop the process or make the
route that needs them fail loudly.

| Variable | Absent means |
|---|---|
| `STORAGE_DEFAULT` + a bucket | **Startup fails.** There is no implicit filesystem bucket |
| `API_KEY_REQUIRED` | Treated as false — Falco boots with writes unauthenticated. Set it explicitly |
| `HMAC_REQUIRED` | Treated as false — delivery falls back to API key. Set it explicitly |
| `HMAC_REQUIRE_EXPIRY` | **Delivery answers 500.** The fallback would be accepting signatures that never expire |

`API_KEY_REQUIRED=true` **requires** `HMAC_REQUIRED=true`. The startup validation
rejects the combination, because protecting writes while leaving delivery
unsigned protects nothing that matters.

## Server

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | Listen port |
| `HOST` | `0.0.0.0` | Listen address |
| `MAX_HEADER_BYTES` | `65536` | Header budget per request (the stdlib's is 1 MiB) |
| `MAX_HEADER_VALUE_COUNT` | `100` | Header count per request (the stdlib's is 500) |
| `ENV` | — | `production` turns off development conveniences |

## Storage

| Variable | Meaning |
|---|---|
| `STORAGE_DEFAULT` | Name of the bucket used when a request names none. Required |
| `STORAGE_BUCKET_<NAME>_TYPE` | `filesystem`, `s3`, `r2` or `jay`. Defines a bucket called `<name>` |
| `STORAGE_BUCKET_<NAME>_PATH` | Filesystem directory |
| `STORAGE_BUCKET_<NAME>_BUCKET` | Remote bucket name |
| `STORAGE_BUCKET_<NAME>_REGION` | S3 region |
| `STORAGE_BUCKET_<NAME>_ENDPOINT` | S3-compatible endpoint. Setting it switches to path-style addressing; the scheme decides TLS |
| `STORAGE_BUCKET_<NAME>_ACCOUNT_ID` | R2 account |
| `STORAGE_BUCKET_<NAME>_ACCESS_KEY` / `_SECRET_KEY` | Credentials |
| `STORAGE_BUCKET_<NAME>_ADDR` / `_ADMIN_ADDR` | Jay's native and admin addresses |
| `STORAGE_BUCKET_<NAME>_TOKEN_ID` / `_TOKEN_SECRET` | Jay credentials |
| `STORAGE_BUCKET_<NAME>_POOL_SIZE` | Jay connection pool size |
| `STORAGE_BUCKET_<NAME>_BACKUP_<N>_TARGET` / `_MODE` | Backup target and mode — see [Backups](/falco/guides/backups/) |
| `STORAGE_BUCKET_<NAME>_KEY_<KEYNAME>_KEY` | A key scoped to this bucket |
| `STORAGE_GROUP_<NAME>_BUCKETS` | Comma-separated buckets in a group |
| `STORAGE_GROUP_<NAME>_KEY_<KEYNAME>_KEY` | A key scoped to the group |
| `STORAGE_GROUP_<NAME>_SUBGROUP_<SUB>_BUCKETS` | Subgroup membership |
| `STORAGE_GROUP_<NAME>_SUBGROUP_<SUB>_KEY_<KEYNAME>_KEY` | A key scoped to the subgroup |

The full shape, in YAML and in variables, is in
[Buckets and groups](/falco/guides/buckets/).

## Processing

| Variable | Default | Meaning |
|---|---|---|
| `MAX_FILE_SIZE_MB` | `10` | Upload body cap |
| `DEFAULT_QUALITY` | `85` | Encode quality when no `q` is given |
| `DEFAULT_FORMAT` | `webp` | Stored and served format when nothing else decides |
| `CONCURRENT_WORKERS` | `4` | Simultaneous transformations. Beyond this, requests queue |
| `WEBP_EFFORT` | `4` | libvips WebP effort, 0–6. Higher is smaller and slower |

## Cache

| Variable | Default | Meaning |
|---|---|---|
| `CACHE_SIZE_MB` | `256` | In-memory ceiling. `0` disables the cache entirely |
| `CACHE_TTL_HOURS` | `24` | Per-entry lifetime |
| `CACHE_CLEANUP_INTERVAL` | `10m` | How often expired entries are swept |
| `CACHE_DEFAULT_MAX_AGE` | `31536000` | `Cache-Control: max-age` |
| `CACHE_DEFAULT_SMAX_AGE` | `31536000` | `Cache-Control: s-maxage` |
| `ENABLE_REDIS` | `false` | Second cache layer |
| `REDIS_URL` | — | e.g. `redis://valkey:6379/0` |

`CACHE_TTL_HOURS` and `CACHE_CLEANUP_INTERVAL` are different knobs. Confusing
them has already made the TTL a no-op once.

## Security

| Variable | Default | Meaning |
|---|---|---|
| `API_KEY_REQUIRED` | *(none)* | Authenticate writes |
| `API_KEY` | — | The admin key: unrestricted access |
| `HMAC_REQUIRED` | *(none)* | Require signed delivery URLs |
| `HMAC_KEY` | — | Signing key, hex. `openssl rand -hex 32` |
| `HMAC_SALT` | — | Signing salt, hex |
| `HMAC_SIGNATURE_SIZE` | `32` | Truncation length in bytes |
| `HMAC_REQUIRE_EXPIRY` | *(none)* | Reject signatures with no `exp` |
| `TRUSTED_PROXIES` | *(loopback only)* | CIDRs whose `X-Forwarded-For` is believed |
| `CORS_ORIGINS` | `localhost` patterns | Comma-separated allowed origins |
| `RATE_LIMIT_RPM` | `1000` | Requests per minute per IP. `0` disables |

## Proxy

Read directly from the environment, not through the config file:

| Variable | Default | Meaning |
|---|---|---|
| `PROXY_ALLOWED_HOSTS` | A compiled-in list | Hosts the proxy may fetch from |
| `PROXY_MAX_WIDTH` | `600` | Width cap when neither `w` nor `h` is given |
| `PROXY_DEFAULT_QUALITY` | `75` | Quality when no `q` is given |

## Watermark

| Variable | Default | Meaning |
|---|---|---|
| `WATERMARK_ALLOWED_HOSTS` | *(none)* | Hosts `wm_url` may fetch an overlay from. **Unset means external watermarks are refused**, not that any host is allowed |

A watermark that lives in Falco's own storage (`wm=logos/brand`) needs no
configuration and makes no outbound request. See
[Transformations](/falco/reference/transformations/).

## Logging and diagnostics

| Variable | Default | Meaning |
|---|---|---|
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` or `text` |
| `LOG_OUTPUT` | `stdout` | Destination |
| `DEBUG` | `false` | Development mode |
| `ENABLE_METRICS` | `true` | Mount `/metrics` behind the API key |
| `ENABLE_PPROF` | `false` | Mount `/debug/pprof/*` behind the API key |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | Unset means telemetry is skipped, not failed |
| `OTEL_DEPLOYMENT_ENV` | — | Environment tag on the traces |

Falco logs its whole configuration at startup with secrets redacted, which is
usually the fastest way to find out that a variable you thought you set did not
reach the process.
