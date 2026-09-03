---
title: Deploying Falco
description: A compose file worth copying, what to turn on before it faces the internet, and what sits in front of it.
---

## Compose

```yaml
services:
  falco:
    image: ghcr.io/birdple/falco:0.13.0
    ports:
      - "8080:8080"
    volumes:
      - falco-data:/app/data
    environment:
      PORT: 8080
      HOST: 0.0.0.0

      STORAGE_DEFAULT: local
      STORAGE_BUCKET_LOCAL_TYPE: filesystem
      STORAGE_BUCKET_LOCAL_PATH: /app/data/images

      API_KEY_REQUIRED: "true"
      API_KEY: ${API_KEY:?set API_KEY}
      HMAC_REQUIRED: "true"
      HMAC_KEY: ${HMAC_KEY:?set HMAC_KEY}
      HMAC_SALT: ${HMAC_SALT:?set HMAC_SALT}
      HMAC_REQUIRE_EXPIRY: "true"

      CORS_ORIGINS: https://yourdomain.com
      TRUSTED_PROXIES: 10.0.0.0/8
      LOG_FORMAT: json
      ENABLE_METRICS: "true"
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://localhost:8080/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 40s

volumes:
  falco-data:
```

The `${VAR:?}` form matters: it stops the stack rather than starting a Falco
whose signing key is the empty string.

## Four things to get right

**Turn both auth flags on.** Neither `API_KEY_REQUIRED` nor `HMAC_REQUIRED` has a
default, and absent means false — a Falco deployed without them is open. Setting
`API_KEY_REQUIRED=true` *forces* `HMAC_REQUIRED=true`: the startup validation
refuses the combination where writes are protected and delivery is not, because
an `<img>` tag cannot carry an API key. See
[Authentication](/falco/reference/authentication/).

**Set `TRUSTED_PROXIES`.** Empty means only loopback is trusted, and
`X-Forwarded-For` from anywhere else is ignored — fail-closed by design. Behind
Nginx, Traefik or an ELB, list the proxy's subnet or the per-IP rate limiter
counts every request against the proxy instead of the client.

**Pin the tag.** `:latest` moves on every release. `:0.13.0` and `:0.13` do not
move backwards.

**Put a CDN in front.** Falco caches transformations in memory, which a restart
loses entirely. `s-maxage` is what keeps that from being felt: see
[Caching](/falco/internals/caching/).

## Behind a reverse proxy

Falco caps request headers tighter than the standard library does (64 KiB and 100
values, against 1 MiB and 500). If your proxy adds many headers, raise
`MAX_HEADER_BYTES` and `MAX_HEADER_VALUE_COUNT` rather than discovering the limit
as a 431.

Give the proxy a generous body limit on `/api/v1/upload` — `MAX_FILE_SIZE_MB`
(10 by default) is what Falco itself enforces, and a proxy that cuts in lower
turns a clear `413` from Falco into a confusing one from the proxy.

`robots.txt` disallows everything, on purpose: Falco is a CDN origin, not
indexable content.

## Health

`GET /health` needs no authentication — it is what an orchestrator polls — and
answers with the version that is actually running:

```json
{"status":"healthy","version":"0.13.0","uptime":"3h12m4s"}
```

That version comes from the build, so it is also the fastest way to tell whether
a deploy actually rolled out.

## Storage on a real deployment

The filesystem bucket in the compose above is the simplest thing that works and
ties the service to one machine's disk. For anything with more than one replica,
point it at S3, R2 or Jay — see [Buckets and groups](/falco/guides/buckets/) — and
keep the volume only for the cache directory it does not need.
