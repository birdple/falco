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

A value that does not parse **stops the boot** rather than being skipped:
`API_KEY_REQUIRED=yes` or `PORT=eighty` is a startup error naming the variable,
not a silently ignored setting. Booleans take `true`/`false` (or `1`/`0`).

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
| `API_KEY_REQUIRED` | Treated as false — Falco boots with the API unauthenticated. Set it explicitly |
| `HMAC_REQUIRED` | Treated as false — delivery and the proxy are open. Set it explicitly |
| `HMAC_REQUIRE_EXPIRY` | With `HMAC_REQUIRED=true`, **delivery and the proxy answer 500.** The fallback would be accepting signatures that never expire |

`API_KEY_REQUIRED` and `HMAC_REQUIRED` **must agree**. The startup validation
rejects either one without the other: protecting writes while leaving delivery
unsigned protects nothing that matters, and requiring signatures while `/sign`
is open lets anyone mint them. See [Authentication](/falco/reference/authentication/).

## Server

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | Listen port |
| `HOST` | `0.0.0.0` | Listen address |
| `MAX_HEADER_BYTES` | `65536` | Header budget per request (the stdlib's is 1 MiB) |
| `MAX_HEADER_VALUE_COUNT` | `100` | Header count per request (the stdlib's is 500) |
| `SERVER_SHUTDOWN_TIMEOUT` | `30s` | How long a shutdown waits for in-flight requests and async replications. Keep it under the orchestrator's grace period (Docker's is 10s) |

Shutdown drains HTTP first, then releases storage (waiting for async backup
replications), then flushes telemetry on its own 5-second budget. A second
`SIGINT`/`SIGTERM` exits immediately.

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
| `STORAGE_BUCKET_ALIASES` | Comma-separated `alias=bucket` pairs. Lets a client keep sending a name that is not a bucket — see [Buckets and groups](/falco/guides/buckets/). An alias pointing nowhere, shadowing a bucket, or missing its `=` stops the boot |
| `STORAGE_GROUP_<NAME>_BUCKETS` | Comma-separated buckets in a group |
| `STORAGE_GROUP_<NAME>_KEY_<KEYNAME>_KEY` | A key scoped to the group |
| `STORAGE_GROUP_<NAME>_KEY_<KEYNAME>_BUCKETS` | Narrows that key to a subset of the group |
| `STORAGE_GROUP_<NAME>_SUBGROUP_<SUB>_BUCKETS` | Subgroup membership |
| `STORAGE_GROUP_<NAME>_SUBGROUP_<SUB>_KEY_<KEYNAME>_KEY` | A key scoped to the subgroup |
| `STORAGE_GROUP_<NAME>_SUBGROUP_<SUB>_KEY_<KEYNAME>_BUCKETS` | Narrows that key to a subset of the subgroup |

The full shape, in YAML and in variables, is in
[Buckets and groups](/falco/guides/buckets/).

Scoped keys are checked as a whole at startup, and each of these stops the boot:
a key that resolves to no bucket at all, a group or subgroup key naming a bucket
outside its group or subgroup, the same key value configured twice (under any
two scopes), and a scoped key equal to `API_KEY`.

## Processing

| Variable | Default | Meaning |
|---|---|---|
| `MAX_FILE_SIZE_MB` | `10` | Upload body cap |
| `DEFAULT_QUALITY` | `85` | Encode quality for the lossy encoders when no `q` is given. 1–100 |
| `DEFAULT_FORMAT` | `webp` | Stored and served format when nothing else decides. One of `jpeg`, `png`, `webp`, `avif`, `heic`; anything else stops the boot |
| `CONCURRENT_WORKERS` | `4` | Simultaneous transformations. Beyond this, requests queue |
| `MAX_MEGAPIXELS` | `100` | Pixel ceiling for every decoded input and every output, in millions. Above it the image is refused with `422 IMAGE_TOO_LARGE`. `0` means the built-in default |
| `WEBP_EFFORT` | `4` | libvips WebP effort, 0–6. Higher is smaller and slower |

There is no list of supported formats to configure. Output is always one of the
five above; input is decoded only from JPEG, PNG, WebP, GIF, HEIF/HEIC, AVIF and
TIFF, and libvips' untrusted loaders are blocked (`VIPS_BLOCK_UNTRUSTED` is set
at startup unless you set it yourself).

## Cache

| Variable | Default | Meaning |
|---|---|---|
| `CACHE_SIZE_MB` | `256` | In-memory ceiling. `0` disables the in-process cache; negative stops the boot |
| `CACHE_TTL_HOURS` | `24` | Per-entry lifetime |
| `CACHE_CLEANUP_INTERVAL` | `10m` | How often expired entries are swept. A Go duration; `0` uses the default, negative stops the boot |
| `CACHE_DEFAULT_MAX_AGE` | `31536000` | `Cache-Control: max-age` |
| `CACHE_DEFAULT_SMAX_AGE` | `31536000` | `Cache-Control: s-maxage` |
| `ENABLE_REDIS` | `false` | Use Redis as the cache instead of the in-process LRU |
| `REDIS_URL` | — | e.g. `redis://valkey:6379/0`. Logged with its password redacted |

A Redis that cannot be reached at startup falls back to the in-process LRU
(unless `CACHE_SIZE_MB=0`, in which case there is no cache at all).

`CACHE_TTL_HOURS` and `CACHE_CLEANUP_INTERVAL` are different knobs. Confusing
them has already made the TTL a no-op once.

## Security

| Variable | Default | Meaning |
|---|---|---|
| `API_KEY_REQUIRED` | *(none)* | Authenticate the API. Requires `HMAC_REQUIRED=true` |
| `API_KEY` | — | The admin key: unrestricted access. No scoped key may reuse its value |
| `HMAC_REQUIRED` | *(none)* | Require signed delivery and proxy URLs. Requires `API_KEY_REQUIRED=true` |
| `HMAC_KEY` | — | Signing key, hex (validated at startup). `openssl rand -hex 32` |
| `HMAC_SALT` | — | Signing salt, hex (validated at startup) |
| `HMAC_SIGNATURE_SIZE` | `32` | Truncation length in bytes: `0` (full) or 16–32. Anything else stops the boot |
| `HMAC_REQUIRE_EXPIRY` | *(none)* | Reject signatures with no `exp` |
| `TRUSTED_PROXIES` | *(loopback only)* | CIDRs whose `X-Forwarded-For` is believed |
| `CORS_ORIGINS` | `localhost` patterns | Comma-separated allowed origins |
| `RATE_LIMIT_RPM` | `1000` | Requests per minute per client (IPv6 by `/64`), plus a burst of 100. `0` disables |
| `COOKIE_SECURE` | `false` | Force the `Secure` flag on the panel's cookies. Without it the flag follows the request, which falco cannot see when TLS ends at a proxy |

Startup logs a warning for any API key, admin or scoped, shorter than 24
characters.

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
| `ENABLE_METRICS` | `false` | Mount `/metrics` behind the admin key. Only with `API_KEY_REQUIRED=true`; otherwise it is not mounted at all |
| `ENABLE_PPROF` | `false` | Mount `/debug/pprof/*` behind the admin key. Same condition |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | Unset (and no `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` either) means telemetry is **off**: no exporter is started |
| `OTEL_DEPLOYMENT_ENV` | `development` | Environment tag on the traces |

`ENV` and `DEBUG` are no longer read; neither changed any behaviour.

Falco logs its effective configuration at startup — bucket and group names,
port, cache, processing and auth flags, never a credential — which is usually
the fastest way to find out that a variable you thought you set did not reach
the process.
