---
title: Backups
description: Any bucket can replicate to any other bucket, in three modes with three different failure stories.
---

A bucket can name other buckets as backup targets. Because every target is just a
bucket, "back up S3 to R2" and "back up the local disk to Jay" are the same
feature.

```yaml
storage:
  buckets:
    images:
      type: s3
      bucket: prod-images
      backups:
        - target: hot-backup
          mode: sync
        - target: cold-archive
          mode: async
        - target: legacy
          mode: read-fallback
```

The targets must be buckets defined in the same config, and a bucket cannot back
up to itself. Both are checked at startup, so a typo stops the process instead of
producing a deployment that thinks it has a backup.

## The three modes

| Mode | Write | Read | Fails when |
|---|---|---|---|
| `sync` | Primary **and** target, before answering | Primary | The target write fails — the whole upload fails |
| `async` | Primary, then the target in the background | Primary | Never, from the caller's point of view |
| `read-fallback` | Primary only | Primary, then the target on a 404 | Never |

**`sync`** is the only one that lets you say the image is in two places when you
answer the client. It is also the one that makes your uploads as slow, and as
available, as your least reliable backend.

**`async`** is best-effort by construction. Replication happens on a goroutine
after the response has gone out: a crash between the two loses the copy, and
nothing tells the caller. Use it for a second copy you would like to have, never
for one you are counting on.

**`read-fallback`** writes nothing. It exists for migrations: point the new
bucket at the old one, and objects that have not moved yet are still served while
you copy them across in the background.

## Environment variables

```bash
STORAGE_BUCKET_IMAGES_BACKUP_1_TARGET=hotbackup
STORAGE_BUCKET_IMAGES_BACKUP_1_MODE=sync
STORAGE_BUCKET_IMAGES_BACKUP_2_TARGET=coldarchive
STORAGE_BUCKET_IMAGES_BACKUP_2_MODE=async
```

Numbered from 1, any number of them, mixing modes and providers freely.

:::note[The async goroutines are visible]
Background replication runs on `context.Background()` and is awaited by no
`WaitGroup`, which means shutdown does not consider it. That is what the
`goroutineleak` profile is mounted for — see
[Observability](/falco/reference/observability/).
:::
