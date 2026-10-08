---
title: HTTP API
description: Every route Falco mounts, what guards it, and what it answers.
---

| Method | Path | Guard | Purpose |
|---|---|---|---|
| `GET` | `/health`, `HEAD /health` | — | Liveness, version, commit, uptime |
| `GET` | `/health/ready`, `HEAD` | Admin key | Every backend, its breaker, cache, and what is switched off |
| `GET` | `/robots.txt` | — | Disallows everything |
| `GET` | `/docs` | — | OpenAPI viewer |
| `GET` | `/docs/openapi.yaml` | — | The spec itself |
| `GET` | `/metrics` | Admin key | Prometheus, when `ENABLE_METRICS` **and** `API_KEY_REQUIRED` |
| `GET` | `/debug/pprof/*` | Admin key | Profiles, when `ENABLE_PPROF` **and** `API_KEY_REQUIRED` |
| `GET`, `HEAD` | `/api/v1/images/*` | Signature | [Deliver and transform](/falco/guides/delivering/) |
| `GET` | `/api/v1/proxy/*` | Signature + host allowlist | [Proxy an external image](/falco/guides/proxy/) |
| `POST` | `/api/v1/upload` | Key + scope | [Upload](/falco/guides/uploading/) |
| `POST` | `/api/v1/update` | Key + scope | Replace an object from a URL |
| `GET` | `/api/v1/list` | Key + scope | List stored objects |
| `DELETE` | `/api/v1/delete` | Key + scope | Delete an object or a directory |
| `POST` | `/api/v1/sign` | Key + scope | [Mint a signed URL](/falco/guides/signed-urls/) |
| `GET` | `/api/v1/meta/*` | Key + scope | An object's stored metadata, without transferring it |
| `GET` | `/api/v1/stats` | Key + scope | Object counts and sizes per bucket the key can reach |
| `GET` | `/api/v1/cache` | Key + scope | Transform cache: hit ratio, size, TTL |
| `DELETE` | `/api/v1/cache` | Admin key | Purge every variant, or one key's |

Delivery and proxy sit **outside** the authenticated group on purpose: they
authorise themselves, by HMAC signature when `HMAC_REQUIRED=true`, because a
browser cannot attach an API key to an `<img>` URL. With `HMAC_REQUIRED=false`
they are open — and so is everything else, since the startup validation only
allows that together with `API_KEY_REQUIRED=false`. See
[Authentication](/falco/reference/authentication/).

"Admin key" means `API_KEY` itself: a scoped key is refused there with `401`.
`/metrics` and `/debug/pprof/*` are not mounted at all without
`API_KEY_REQUIRED=true`, rather than served in the open.

## The admin panel

| Method | Path | Guard | Purpose |
|---|---|---|---|
| `GET` | `/` | — | Sign in |
| `POST` | `/ui/auth` | Same-origin JSON | Exchange an API key for a session |
| `POST` | `/ui/logout` | Same-origin | End the session server-side |
| `POST` | `/ui/theme` | Same-origin | Remember light/dark |
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

`/ui/auth` only accepts a same-origin request with a JSON body
(`{"key": "…"}`): a cross-site form cannot send one without a preflight, so
another site cannot sign a visitor into the panel under its own key. Failed
sign-ins are throttled per client — after 10 failures in 15 minutes the answer
is `429` with `Retry-After` — on top of the global rate limit. One key holds at
most 20 live sessions; signing in a 21st time drops the oldest. Sessions last 12
hours.

The cookie is `Secure` when the request arrived over TLS. Behind a proxy that
terminates TLS, falco cannot see that, so set `COOKIE_SECURE=true`.

The panel grants nothing of its own: it resolves the session to a scope,
publishes that scope into the request context, and hands mutations to the same
API handlers listed above. Anything the API would refuse, the panel refuses too.

**With no API key configured the panel refuses to serve** — `/` explains which
variable is missing and the rest answers `403` (`/ui/auth` answers `503`). It
does not fall open.

## List

```bash
curl -H "X-API-Key: $KEY" "localhost:8080/api/v1/list?b=images&d=avatars"
```

| Parameter | Aliases | Meaning |
|---|---|---|
| `b` | `bucket` | Bucket to list. `storage` is accepted too |
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

A prefix is a **directory**: `d=users/1` lists `users/1/…` and never
`users/10/…`.

`truncated` says whether more remains; `next_cursor` is opaque and is passed
back verbatim. Directories in a paginated answer carry no `file_count` (it is
`null`): counting would mean walking the whole prefix, which is what paging
avoids. A backend that cannot page answers `PAGINATION_UNSUPPORTED` instead of
quietly returning everything as if it were a page.

## Delete

The body names what to delete: a list of `keys`, a `prefix`, or both, plus an
optional `bucket` (or `storage`). It is a JSON body, not query parameters.

```bash
# Objects by key
curl -X DELETE localhost:8080/api/v1/delete \
  -H "X-API-Key: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"bucket": "images", "keys": ["a1b2c3d4", "avatars/f3a9"]}'

# A whole directory
curl -X DELETE localhost:8080/api/v1/delete \
  -H "X-API-Key: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"prefix": "products/thumbnails"}'
```

```json
{ "success": true, "deleted": ["a1b2c3d4", "avatars/f3a9"], "count": 2 }
```

A prefix is a directory, the same as in a listing: `users/1` takes
`users/1/…` and leaves `users/10/…` alone. A prefix that names no directory at
all — `/` or `//` — is `400 INVALID_PREFIX`: deleting a whole bucket is not
something this endpoint does. A prefix over 100 000 objects is
`400 LISTING_TOO_LARGE` rather than a partial delete.

A key that was already gone counts as neither deleted nor failed. A key that
could not be deleted — a backend error, or a scoped key that does not own it —
lands in `failed`, and the response is `207` with `"success": false`, so a
caller can retry exactly what is left. `truncated: true` means the prefix may
hold more than the listing could see.

Deleting removes the stored object. Cached transformations of every key touched
are dropped along with it, but anything a CDN already holds lives out its
`s-maxage`.

## Ownership

Uploads record the caller's `X-Owner-Id` header, if any, as the object's owner.
For a **scoped** key it matters on every mutation: `update` and `delete` require
the same `X-Owner-Id` as the stored owner, and an object with no recorded owner
can only be changed by the admin key. Uploading over an object someone else owns
is `409 OBJECT_EXISTS` with a caller-chosen `?id=`; with a content-hash id the
bytes are identical by definition, so the existing object is returned untouched.
The admin key bypasses all of it.

`GET /api/v1/meta/*` reports `owner_id` only to the admin key, or to a caller
already presenting that owner in `X-Owner-Id` — handing it to any key that can
read the bucket would hand it exactly the header these checks trust.

## Cache

`GET /api/v1/cache` reports hits, misses, size, item count, TTL and sweep
interval of the transform cache. `DELETE /api/v1/cache` purges it, and needs the
admin key:

```bash
# Everything
curl -X DELETE -H "X-API-Key: $KEY" localhost:8080/api/v1/cache
# Every variant of one stored key, in a bucket (the default bucket without ?b=)
curl -X DELETE -H "X-API-Key: $KEY" "localhost:8080/api/v1/cache?key=avatars/a1b2c3d4&b=images"
```

```json
{ "success": true, "purged": 3, "scope": "key", "key": "avatars/a1b2c3d4" }
```

Cache entries belong to a bucket, so a per-key purge names one: `?b=` or
`?storage=`, the default bucket when neither is given.

## Errors

Every error — from a handler or from the middleware in front of it — is JSON
with a stable machine-readable `code`, and carries `Cache-Control: no-store` so
no CDN keeps it:

```json
{
  "success": false,
  "error": { "code": "INVALID_WIDTH", "message": "must be at least 16 pixels" }
}
```

There is no `data` object on errors. Read `success`, and `error` when it is
false.

Branch on `code`, never on `message`. The messages are written for humans and
change with them.

| Code | Status | Where |
|---|---|---|
| `UNAUTHORIZED` | 401 | Missing or wrong API key |
| `RATE_LIMITED` | 429 | Over `RATE_LIMIT_RPM` for this client. Comes with `Retry-After` |
| `REQUEST_TOO_LARGE` | 413 | Upload body over `MAX_FILE_SIZE_MB` (any route: a declared body over twice that) |
| `ACCESS_DENIED` | 403 | Valid key, wrong bucket for its scope; or a scoped key purging the cache |
| `FORBIDDEN` | 403 | `update` on an object the scoped caller does not own |
| `OBJECT_EXISTS` | 409 | Scoped upload with a custom `?id=` over another owner's object |
| `UNKNOWN_BUCKET` | 400 | `?b=`/`?storage=` names something that is neither a [bucket nor a declared alias](/falco/guides/buckets/), and the backend cannot switch to it |
| `INVALID_SIGNATURE` | 403 | Missing, wrong or expired `sig` |
| `HOST_NOT_ALLOWED` | 403 | Proxy target host not in the allowlist |
| `INVALID_WIDTH`, `INVALID_HEIGHT`, `INVALID_QUALITY`, `INVALID_FORMAT`, `INVALID_FIT` | 400 | Malformed geometry or encoding parameter |
| `INVALID_CROP`, `INVALID_ROTATE`, `INVALID_FLIP` | 400 | Malformed crop, rotation or flip |
| `INVALID_WATERMARK` | 400 | Both `wm` and `wm_url` given, a `wm` that is not a valid id, or a malformed `wm_url` |
| `WATERMARK_HOST_NOT_ALLOWED` | 403 | `wm_url` host is not allowlisted, or the allowlist is empty |
| `WATERMARK_NOT_FOUND` | 404 | `wm` names an image that is not in the bucket |
| `WATERMARK_NOT_AN_IMAGE`, `WATERMARK_TOO_LARGE` | 422 | The overlay is not an image, or is over 2 MB |
| `WATERMARK_FETCH_FAILED` | 502 | The allowlisted host did not serve the overlay, or resolves to a private address |
| `INVALID_ID`, `INVALID_DIRECTORY`, `INVALID_PATH`, `INVALID_PREFIX` | 400 | Malformed id, directory, signing path or delete/list prefix |
| `INVALID_EXPIRY` | 400 | `/sign` asked for an expiry more than 366 days away |
| `INVALID_JSON`, `INVALID_REQUEST` | 400 | Bad body. Unknown fields are rejected, not ignored |
| `MISSING_PARAMETERS`, `MISSING_URL`, `MISSING_BUCKET`, `MISSING_KEY`, `MISSING_FILE` | 400 | A required field is absent |
| `INVALID_LIMIT` | 400 | `?limit=` is not a positive integer, or is over 1000 |
| `LISTING_TOO_LARGE` | 400 | The prefix holds more than 100 000 objects: list it a page at a time, or delete a narrower one |
| `PAGINATION_UNSUPPORTED` | 501 | `?limit=`/`?cursor=` on a backend that cannot page |
| `UNSUPPORTED_CONTENT_TYPE` | 400 | Upload body is not something Falco takes |
| `DOWNLOAD_FAILED` | 400 | Upload or update from a URL could not fetch it |
| `DANGEROUS_CONTENT_TYPE` | 415 | SVG, HTML or XML upload |
| `IMAGE_NOT_FOUND`, `NOT_FOUND` | 404 | No such object in that bucket (`NOT_FOUND` is `update`'s) |
| `UNSUPPORTED_IMAGE` | 415 | The stored or fetched image is in a format libvips is not allowed to decode |
| `IMAGE_TOO_LARGE` | 422 | The image, or the requested output, is over `MAX_MEGAPIXELS` (`413` on the proxy when the upstream body is over 10 MB) |
| `PROCESSING_FAILED` | 422 | libvips could not decode or encode it |
| `PROCESSING_BUSY` | 503 | No processing slot freed up in time. Retry later |
| `STORAGE_UNAVAILABLE` | 503 | The bucket's circuit breaker is open. Retry later |
| `RETRIEVAL_ERROR`, `STORAGE_ERROR` | 500 | The backend failed in some other way (`RETRIEVAL_ERROR` on `update` when the ownership lookup itself fails) |
| `SIGNING_DISABLED` | 501 | `/sign` called with no `HMAC_KEY` configured |
| `CONFIG_ERROR` | 500 | `HMAC_REQUIRE_EXPIRY` unset or unparseable |

JSON bodies are parsed strictly: an unknown field is a `400`, so a typo in
`{"qualty": 90}` is reported instead of silently ignored. Field names are
case-sensitive.

The proxy has a few codes of its own (`MISSING_SEGMENT`, `INVALID_EXTENSION`,
`URL_TOO_LONG`, `UPSTREAM_ERROR`, `FETCH_FAILED`, `NOT_AN_IMAGE`); see
[Proxying external images](/falco/guides/proxy/#when-it-refuses).

## OpenAPI

`/docs/openapi.yaml` is embedded in the binary and served from any deployment,
with a viewer at `/docs`. It is written by hand and covers every route above
except the admin panel's; when it and this site disagree, this site is checked
against the code more often.
