---
title: Signed URLs
description: HMAC signing, why it exists, and the expiry policy that refuses to have a default.
---

A transformation costs CPU. An unsigned delivery URL means anyone who knows an id
can ask for ten thousand distinct sizes of it and make your server encode every
one. HMAC signing is the answer: the signature covers the path **and** the query
string, so the only transformations that exist are the ones you minted.

## Turning it on

```bash
HMAC_KEY=$(openssl rand -hex 32)
HMAC_SALT=$(openssl rand -hex 16)
HMAC_REQUIRED=true
HMAC_REQUIRE_EXPIRY=true
HMAC_SIGNATURE_SIZE=32
```

With `HMAC_REQUIRED=true` the delivery route is **public by signature**: no API
key is involved, because a browser cannot attach one to an `<img>` URL. With it
false, delivery falls back to API key plus scope, so it is never simply open.

:::caution[`HMAC_REQUIRE_EXPIRY` has no default on purpose]
It is read directly from the environment, and if it is missing or unparseable
delivery answers **500**, not "false". The silent fallback would be accepting
signed URLs that never expire — a leaked URL that works forever. Set it
explicitly, in every environment.
:::

## Minting a URL

```bash
curl -X POST localhost:8080/api/v1/sign \
  -H "X-API-Key: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"path": "/api/v1/images/a1b2c3d4?w=800&f=webp", "expires_in": 3600}'
```

```json
{
  "signed_url": "/api/v1/images/a1b2c3d4?w=800&f=webp&exp=1789456123&sig=Yk3...",
  "signature": "Yk3...",
  "expires_at": 1789456123
}
```

`expires_in` (seconds from now) and `expires_at` (Unix seconds) are mutually
exclusive. Give neither and the URL carries no expiry at all — which delivery
accepts only when `HMAC_REQUIRE_EXPIRY=false`.

Signing honours the caller's scope: a key restricted to one bucket cannot sign a
URL for another. Without that check, scoped keys would be a lock on the front
door with the window open.

If `HMAC_KEY` is empty, `/sign` answers `501 SIGNING_DISABLED` rather than
handing back an unsigned path.

## Signing it yourself

Most callers never hit `/sign` — they compute the signature in their own process,
which saves a round trip per image. The canonical form is what matters:

1. Take the path plus query string.
2. Remove `sig` entirely. Keep `exp` if present — it is signed too, so nobody can
   push out the expiry by editing the URL.
3. Sort the remaining parameters.
4. HMAC-SHA256 over that string with `HMAC_KEY`, salted with `HMAC_SALT`,
   truncated to `HMAC_SIGNATURE_SIZE` bytes, then base64url without padding.

The Go implementation is `internal/security/signature.go`. There are two
TypeScript ports in the birdple monorepo, each with byte-for-byte parity tests
against it, because **changing the canonicalisation breaks every client at
once** — and the symptom is a `403 INVALID_SIGNATURE` in production, not a red
test.

## Verifying

Delivery rejects with `403 INVALID_SIGNATURE` when the signature is missing,
does not match, or the `exp` has passed. The comparison is constant-time.

The signature covers the whole query string, so a client cannot change `w=400`
to `w=4000` on a URL you signed. It also means every distinct transformation
needs its own signature — which is the intended cost model.
