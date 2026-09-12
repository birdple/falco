---
title: Buckets and groups
description: Every storage target is a named bucket; groups organise them and keys are scoped to either.
---

Falco has one storage model: **every target is a named bucket**, whatever is
behind it. Buckets can be gathered into groups, groups can have one level of
subgroups, and API keys can be attached at any of those levels.

## Four kinds of bucket

```yaml
storage:
  default: images        # required — Falco will not start without it

  buckets:
    images:
      type: s3
      bucket: prod-images
      region: us-west-2
      access_key: ""
      secret_key: ""

    archive:
      type: r2
      bucket: archive-images
      account_id: ""
      access_key: ""
      secret_key: ""

    fast:
      type: jay
      addr: jay:4012
      admin_addr: jay:4011
      bucket: images
      token_id: ""
      token_secret: ""
      pool_size: 8

    scratch:
      type: filesystem
      path: ./data/images
```

- **`filesystem`** — a directory. Path traversal is validated on every access.
- **`s3`** — Amazon S3 or anything that speaks it. Set `endpoint` for MinIO,
  Ceph or LocalStack: that switches the client to path-style addressing, which is
  what those servers expect. There is no separate `minio` type, and TLS is not a
  flag — the scheme in `endpoint` decides it.
- **`r2`** — Cloudflare R2, which needs `account_id` rather than a region.
- **`jay`** — [Jay](https://github.com/ivangsm/jay) over its binary protocol,
  with a connection pool. This is what birdple runs.

## The same thing in environment variables

Buckets are discovered by pattern, so no list needs to be maintained anywhere:

```bash
STORAGE_DEFAULT=images
STORAGE_BUCKET_IMAGES_TYPE=s3
STORAGE_BUCKET_IMAGES_BUCKET=prod-images
STORAGE_BUCKET_IMAGES_REGION=us-west-2
STORAGE_BUCKET_IMAGES_ACCESS_KEY=…
STORAGE_BUCKET_IMAGES_SECRET_KEY=…
```

Anything matching `STORAGE_BUCKET_<NAME>_TYPE` defines a bucket called `<name>`
in lower case. The recognised suffixes are `_TYPE`, `_BUCKET`, `_PATH`,
`_REGION`, `_ENDPOINT`, `_ACCOUNT_ID`, `_ACCESS_KEY`, `_SECRET_KEY`, `_ADDR`,
`_ADMIN_ADDR`, `_TOKEN_ID`, `_TOKEN_SECRET` and `_POOL_SIZE`.

The name is lower-cased, so `STORAGE_BUCKET_IMAGES_TYPE` defines `images`. Avoid
underscores in the name itself: `_KEY_` and `_BACKUP_` are reserved infixes for
the forms below, and a bucket called `my_key_store` collides with them. The
convention in practice is a single word — `imagesbackup`, not `images_backup`.

## Choosing one per request

```bash
curl -X POST "localhost:8080/api/v1/upload?b=archive" -H "X-API-Key: $KEY" -F "file=@photo.jpg"
curl "localhost:8080/api/v1/list?b=archive"
curl "localhost:8080/api/v1/images/a1b2c3d4?b=archive&w=400"
```

`?b=` and `?bucket=` are the same parameter; `?storage=` also works. Omit it and
`storage.default` is used.

A name that is not a bucket — and not an alias for one, see below — is answered
`400 UNKNOWN_BUCKET`. Nothing is written and nothing is read. This matters more
than it sounds: `?b=` used to be applied as a *remote bucket switch* on top of
the default backend, which only S3 and R2 implement, so on Jay or the filesystem
the parameter fell on the floor. An upload asking for `archive` was written into
the default bucket, answered `201`, and reported
`"url": "/api/v1/images/<id>?b=archive"` — a URL for a bucket the object was
never in.

Falling back to the default is not a behaviour that can be made safe. The caller
named a destination; answering success from a different one is a lie whether or
not the reader happens to land in the same place.

## Aliases: a name that is not a bucket

An alias declares that a name clients already send stands for a bucket that
exists:

```yaml
storage:
  default: images
  bucket_aliases:
    prod-images: images
    legacy: archive
```

```bash
STORAGE_BUCKET_ALIASES="prod-images=images,legacy=archive"
```

The environment form is one variable rather than the `STORAGE_BUCKET_<NAME>_*`
pattern, because the whole point is to name something that pattern cannot
express: bucket names come from an environment variable suffix, so they cannot
contain a hyphen, while a client's configured value very often does.

An alias resolves to its target everywhere — upload, delivery, list, delete,
metadata — and to the target's name in scope checks, so a key is authorised for
one bucket under one name however many aliases point at it. `?b=prod-images` and
`?b=images` reach the same objects, and the alias is never a second bucket:
`/api/v1/stats` and the panel keep listing `images` once.

Three things are refused at **startup**, not on the first request:

| Configuration | Why it stops the boot |
|---|---|
| Alias pointing at a bucket that is not declared | It would answer `400` on every request, with nothing pointing at the config |
| Alias with the same name as a bucket | Two meanings for one name |
| An entry that is not `alias=bucket` | A typo would silently drop the alias the operator believes is configured |

## Groups and subgroups

Groups exist for one reason: to hand a key access to several buckets at once.

```yaml
storage:
  groups:
    media:
      buckets: ["images", "archive"]
      keys:
        - name: media-team
          key: sk-media-team
        - name: readonly-viewer
          key: sk-viewer
          buckets: ["images"]      # a subset of the group
      subgroups:
        thumbnails:
          buckets: ["images"]      # must be a subset of the parent
          keys:
            - name: thumb-service
              key: sk-thumb
```

Resolution is what you would expect: a bucket-level key reaches that bucket, a
group-level key reaches the group's buckets (or the subset it names), and a
subgroup key reaches the subgroup's. The admin key (`security.api_key`) reaches
everything. Anything else is `403 ACCESS_DENIED`.

The environment-variable form follows the same pattern:

```bash
STORAGE_GROUP_MEDIA_BUCKETS=images,archive
STORAGE_GROUP_MEDIA_KEY_MEDIATEAM_KEY=sk-media-team
STORAGE_GROUP_MEDIA_SUBGROUP_THUMBNAILS_BUCKETS=images
STORAGE_GROUP_MEDIA_SUBGROUP_THUMBNAILS_KEY_THUMBSVC_KEY=sk-thumb
```

Scoping is enforced on upload, list, delete, delivery **and** signing. See
[Authentication](/falco/reference/authentication/).
