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
| `read-fallback` | Primary only | Primary, then the target on a 404 | A delete that cannot remove the target's copy |

**`sync`** is the only one that lets you say the image is in two places when you
answer the client. It is also the one that makes your uploads as slow, and as
available, as your least reliable backend.

**`async`** is best-effort by construction. Replication happens on a goroutine
after the response has gone out: a crash between the two loses the copy, and
nothing tells the caller. At most **64** replications are in flight at once —
each one holds the whole object in memory until its backup answers — and past
that a replication is **dropped**, logged and counted in
`falco_storage_replications_dropped_total` rather than queued, so a slow backup
cannot grow the process until it dies. A graceful shutdown waits for the ones in
flight, up to `SERVER_SHUTDOWN_TIMEOUT`. Use it for a second copy you would like
to have, never for one you are counting on.

**`read-fallback`** writes nothing. It exists for migrations: point the new
bucket at the old one, and objects that have not moved yet are still served while
you copy them across in the background. Deletes are the exception: since reads
fall through to the target, a delete removes the target's copy too,
synchronously, and fails if it cannot — otherwise a deleted image would go on
being served from the backup.

## Environment variables

```bash
STORAGE_BUCKET_IMAGES_BACKUP_1_TARGET=hotbackup
STORAGE_BUCKET_IMAGES_BACKUP_1_MODE=sync
STORAGE_BUCKET_IMAGES_BACKUP_2_TARGET=coldarchive
STORAGE_BUCKET_IMAGES_BACKUP_2_MODE=async
```

Numbered from 1, any number of them, mixing modes and providers freely.

:::note[Watching async replication]
Background replication runs on `context.Background()`, detached from the request
that caused it. Shutdown drains it, but nothing else bounds how long a hung
backup holds a slot. `falco_storage_replications_dropped_total` rising means
backups are going stale; the `goroutineleak` profile shows replications that
never finish — see [Observability](/falco/reference/observability/).
:::
