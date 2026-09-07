---
title: HTTP API
description: Every route Falco mounts, what guards it, and what it answers.
---

| Method | Path | Guard | Purpose |
|---|---|---|---|
| `GET` | `/health`, `HEAD /health` | — | Liveness, version, uptime |
| `GET` | `/health/ready`, `HEAD` | — | Every backend, its breaker, cache, and what is switched off |
| `GET` | `/robots.txt` | — | Disallows everything |
| `GET` | `/docs` | — | OpenAPI viewer |
| `GET` | `/docs/openapi.yaml` | — | The spec itself |
| `GET` | `/metrics` | API key | Prometheus, when `ENABLE_METRICS` |
| `GET` | `/debug/pprof/*` | API key | Profiles, when `ENABLE_PPROF` |
| `GET`, `HEAD` | `/api/v1/images/*` | Signature **or** key + scope | [Deliver and transform](/falco/guides/delivering/) |
| `GET` | `/api/v1/proxy/*` | Allowlist | [Proxy an external image](/falco/guides/proxy/) |
| `POST` | `/api/v1/upload` | Key + scope | [Upload](/falco/guides/uploading/) |
| `POST` | `/api/v1/update` | Key + scope | Replace an object from a URL |
| `GET` | `/api/v1/list` | Key + scope | List stored objects |
| `DELETE` | `/api/v1/delete` | Key + scope | Delete an object or a directory |
| `POST` | `/api/v1/sign` | Key + scope | [Mint a signed URL](/falco/guides/signed-urls/) |
| `GET` | `/api/v1/meta/*` | Key + scope | An object's stored metadata, without transferring it |
| `GET` | `/api/v1/stats` | Key + scope | Object counts and sizes per bucket |
| `GET` | `/api/v1/cache` | Key + scope | Transform cache: hit ratio, size, TTL |
| `DELETE` | `/api/v1/cache` | Admin key | Purge every variant, or one key's |

Delivery and proxy sit **outside** the authenticated group on purpose: they
authorise themselves, because a browser cannot attach an API key to an `<img>`
URL. Everything else is inside it.

## The admin panel

| Method | Path | Guard | Purpose |
|---|---|---|---|
| `GET` | `/` | — | Sign in |
| `POST` | `/ui/auth` | — | Exchange an API key for a session |
| `POST` | `/ui/logout` | — | End the session server-side |
| `POST` | `/ui/theme` | — | Remember light/dark |
| `GET` | `/dashboard` | Session | Storage explorer |
| `GET` | `/object` | Session | One object and its metadata |
| `GET` | `/playground` | Session | Transformation workbench |
| `GET` | `/signer` | Session | Build a signed URL |
| `GET` | `/ops` | Session | Backends, cache, features, effective config |
| `GET` | `/ui/explorer` | Session | The explorer body, for HTMX |
| `POST` | `/ui/objects/upload` | Session + CSRF | Upload |
| `POST` | `/ui/objects/delete` | Session + CSRF | Delete |
| `POST` | `/ui/sign` | Session + CSRF | Sign a path |
| `POST` | `/ui/cache/purge` | Session + CSRF | Purge the cache |
| `GET` | `/static/*` | — | Panel assets, extension allowlist |

Signing in exchanges the API key for an opaque, HttpOnly session cookie; the key
itself stays on the server and never reaches the browser. Mutating routes also
require the session's CSRF token in `X-CSRF-Token`.

The panel grants nothing of its own: it resolves the session to a scope,
publishes that scope into the request context, and hands mutations to the same
API handlers listed above. Anything the API would refuse, the panel refuses too.

**With no API key configured the panel refuses to serve** — `/` explains which
variable is missing and the rest answers `403`. It does not fall open.

## List

```bash
curl -H "X-API-Key: $KEY" "localhost:8080/api/v1/list?b=images&d=avatars"
```

| Parameter | Aliases | Meaning |
|---|---|---|
| `b` | `bucket`, `storage` | Bucket to list |
| `p` | `prefix`, `d`, `dir`, `directory` | Directory or prefix within it |
| `limit` | — | Objects per page, 1–1000. Turns on paginated mode |
| `cursor` | — | `next_cursor` from the previous page. Also turns on paginated mode |

```json
{
  "success": true,
  "count": 1,
  "files": [
    { "key": "6d556268ff5afc0f", "size": 278, "modified": "2026-09-03T00:33:32Z" }
  ],
  "truncated": false
}
```

### Whole prefix, or one page

Without `limit` and `cursor` the answer is the **entire** prefix: Falco walks
every page of the backend's listing and `truncated` is always `false`. It used
to ask the backend for one page of 1000 keys and drop the "there is more" flag,
so a large directory came back short with `success: true` and nothing said so.

That walk is bounded at **100 000 objects**. A prefix bigger than that is
answered with `LISTING_TOO_LARGE` rather than with the part that fit — a clipped
listing is indistinguishable from a complete one, and a caller deleting from it
would leave objects behind believing it was done.

With `limit` or `cursor`, the answer is one page:

```bash
curl -H "X-API-Key: $KEY" "localhost:8080/api/v1/list?d=avatars&limit=200"
# → { "truncated": true, "next_cursor": "avatars/f3a…", … }
curl -H "X-API-Key: $KEY" "localhost:8080/api/v1/list?d=avatars&limit=200&cursor=avatars/f3a…"
```

`truncated` says whether more remains; `next_cursor` is opaque and is passed
back verbatim. Directories in a paginated answer carry no `file_count` (it is
`null`): counting would mean walking the whole prefix, which is what paging
avoids. A backend that cannot page answers `PAGINATION_UNSUPPORTED` instead of
quietly returning everything as if it were a page.

## Delete

```bash
# One object
curl -X DELETE localhost:8080/api/v1/delete \
  -H "X-API-Key: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"id": "a1b2c3d4"}'

# A whole directory
curl -X DELETE "localhost:8080/api/v1/delete?d=products/thumbnails" \
  -H "X-API-Key: $KEY"
```

Deleting removes the stored object. Cached transformations of it are dropped
along with it, but anything a CDN already holds lives out its `s-maxage`.

## Errors

Every error is JSON with a stable machine-readable `code`:

```json
{
  "success": false,
  "data": { "id": "", "url": "", "format": "", "size": 0, "…": "" },
  "error": { "code": "INVALID_WIDTH", "message": "must be at least 16 pixels" }
}
```

The empty `data` object rides along on errors too — it is one response type for
both outcomes. Read `success` and `error`, and ignore `data` unless `success` is
true.

Branch on `code`, never on `message`. The messages are written for humans and
change with them.

| Code | Status | Where |
|---|---|---|
| `UNAUTHORIZED` | 401 | Missing or wrong API key |
| `ACCESS_DENIED` | 403 | Valid key, wrong bucket for its scope |
| `UNKNOWN_BUCKET` | 400 | `?b=`/`?storage=` names something that is neither a [bucket nor a declared alias](/falco/guides/buckets/), and the backend cannot switch to it |
| `INVALID_SIGNATURE` | 403 | Missing, wrong or expired `sig` |
| `HOST_NOT_ALLOWED` | 403 | Proxy target not in the allowlist, or private |
| `INVALID_WIDTH`, `INVALID_HEIGHT`, `INVALID_QUALITY`, `INVALID_FORMAT`, `INVALID_FIT` | 400 | Malformed geometry or encoding parameter |
| `INVALID_CROP`, `INVALID_ROTATE`, `INVALID_FLIP` | 400 | Malformed crop, rotation or flip |
| `INVALID_WATERMARK` | 400 | Both `wm` and `wm_url` given, or a `wm` that is not a valid id |
| `WATERMARK_HOST_NOT_ALLOWED` | 403 | `wm_url` host is not allowlisted, or resolves inward |
| `WATERMARK_NOT_FOUND` | 404 | `wm` names an image that is not in the bucket |
| `WATERMARK_NOT_AN_IMAGE`, `WATERMARK_TOO_LARGE` | 422 | The overlay is not an image, or is over 2 MB |
| `WATERMARK_FETCH_FAILED` | 502 | The allowlisted host did not serve the overlay |
| `INVALID_ID`, `INVALID_DIRECTORY`, `INVALID_PATH` | 400 | Malformed id, directory or signing path |
| `INVALID_JSON`, `INVALID_REQUEST` | 400 | Bad body. Unknown fields are rejected, not ignored |
| `INVALID_LIMIT` | 400 | `?limit=` is not a positive integer, or is over 1000 |
| `LISTING_TOO_LARGE` | 400 | The prefix holds more than 100 000 objects: list it a page at a time, or delete a narrower one |
| `PAGINATION_UNSUPPORTED` | 501 | `?limit=`/`?cursor=` on a backend that cannot page |
| `UNSUPPORTED_CONTENT_TYPE` | 400 | Upload body is not something Falco takes |
| `DANGEROUS_CONTENT_TYPE` | 415 | SVG, HTML or XML upload |
| `PROCESSING_FAILED` | 422 | libvips could not decode or encode it |
| `SIGNING_DISABLED` | 501 | `/sign` called with no `HMAC_KEY` configured |
| `CONFIG_ERROR` | 500 | `HMAC_REQUIRE_EXPIRY` unset or unparseable |

JSON bodies are parsed strictly: an unknown field is a `400`, so a typo in
`{"qualty": 90}` is reported instead of silently ignored. Field names are
case-sensitive.

## OpenAPI

`/docs/openapi.yaml` is embedded in the binary and served from any deployment.
It is generated by hand and currently **does not describe `/sign` or `/proxy`** —
this site is the more complete reference.
