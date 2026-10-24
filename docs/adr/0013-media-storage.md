# ADR-0013: Media Storage

## Status

Accepted

## Context

GoModel handles three kinds of binary media and had one place to keep them:
the audit log document.

- `/v1/audio/speech` output and `/v1/audio/transcriptions` uploads were
  base64-encoded into the audit entry's `data` JSON when
  `LOGGING_LOG_AUDIO_BODIES=true`.
- `/v1/images/*` uploads and results were embedded the same way when
  `LOGGING_LOG_IMAGE_BODIES=true`, under an entry-wide byte budget.
- Video generation (issue #1065, roadmap: "storing artifacts") had no home at
  all. Every video API is request-then-poll and the provider deletes the
  artifact after a short time, so the gateway must keep its own copy to be
  useful.

Bytes in the audit document forced a set of caps that existed only to keep
one row under MongoDB's 16 MiB BSON limit: 8 MB of raw audio, 8 MB of encoded
image bytes per entry, and a relayed audio stream past 8 MB kept only its byte
count. A database is also the wrong place for blobs in general: rows bloat,
backups and replication carry pixels, and the dashboard had to move whole
files through JSON to play one clip.

Two patterns already exist in the codebase and shaped the design:

- Every async surface (`/v1/batches`, background Responses) is
  refresh-on-read backed by a small `id, status, user_path, data` table; no
  worker pool.
- Every store has a filesystem-independent metadata row and an hourly
  retention sweep driven by `storage.RunCleanupLoop`.

## Decision

### 1. Two layers: a blob store and a metadata store

`internal/blobstore` holds bytes behind a four-method interface: `Create`
(returns a writer that becomes visible on `Commit` and is discarded on
`Close`), `Open` (a seekable reader), `Delete`, `Close`. Keys are restricted
to `[A-Za-z0-9._/-]` path segments with no `.` or `..`, so a key is safe to
join under a root directory. Two backends ship: `filesystem` (temp file plus
rename, so a reader never sees a partial blob) and `memory` (tests and
ephemeral deployments).

`internal/mediastore` owns the `media_objects` table (SQLite, PostgreSQL,
MongoDB, memory) and pairs it with a blob store in `mediastore.Service`. A row
records `kind` (`audio`, `image`, `video`), `source` (who created it),
`content_type`, `bytes`, `storage_key`, `request_id`, `user_path`,
`created_at`, and `expires_at` (0 = never). The database owns listing,
tenancy scoping, and expiry; the blob store stays opaque.

The blob key is `<kind>/<yyyy>/<mm>/<dd>/<id>.<ext>`. An operator who mounts
the media directory from EFS, a bind mount, or an S3 FUSE mount can browse it
without the database.

### 2. Retention runs on the row, not the file

`mediastore.Service` runs one hourly sweep: rows whose `expires_at` has
passed are deleted together with their blob, in batches. Each object carries
its own TTL, chosen by whoever stores it. Audit-captured media inherits
`LOGGING_RETENTION_DAYS`, so a media file never outlives the audit entry that
references it by more than one sweep interval. A missing blob during the
sweep is not an error: the row is still removed.

### 3. Audit-logged media moves out of the document

`AudioBodyLog` and `ImageItemLog` keep their `__audio__` / `__images__`
markers, content type, byte count, and `stored` flag, and gain `media_id`.
The base64 `encoding` / `data` members and the `too_large` flag are no longer
written. The dashboard keeps rendering rows written before this change, which
still carry inline base64.

Capture streams: a relayed audio body is teed into a blob writer as it passes
to the client, so a response of any size is stored without being held in
memory. A store failure never affects the client; the entry records
`stored: false` and the failure is logged.

The per-entry image budget and the 8 MB audio cap are removed. Disk usage is
bounded by retention instead.

### 4. The dashboard fetches media through an authenticated admin route

`GET /admin/media/{id}` streams a stored object with its content type and
supports range requests, so browser audio players can seek. It applies the
same user-path scope rule as `GET /admin/audit/detail`: an object outside the
caller's scope is reported as missing. The dashboard fetches with its bearer
token and hands the browser an object URL, because `<audio src>` and
`<img src>` cannot carry an `Authorization` header.

### 5. Storage backends are compiled in, not plugins

The plugin system (ADR-0012) models request and response hooks under a
stdlib-only, toolchain-locked `.so` contract. Storage backends belong next to
SQLite, PostgreSQL, and MongoDB as compiled-in choices selected by
configuration. A native S3 backend is the planned third backend and covers
S3, MinIO, Cloudflare R2, and the GCS interoperability API.

### 6. Configuration

```text
MEDIA_STORAGE_TYPE=filesystem   # filesystem | memory
MEDIA_STORAGE_PATH=<data dir>/media
```

The path follows the same rule as the SQLite database: `./data/media` when a
`./data` directory exists next to the process, otherwise the OS per-user data
directory. At startup, when media logging is enabled and the path sits on a
container's own overlay or tmpfs, the gateway warns the same way it does for
the database.

No retention knob is added in this change. Audit media follows the audit
retention; generated media (videos, images returned by URL) will carry their
own retention when those surfaces land.

## Consequences

### Positive

- **No size caps on logged media**: a two-hour transcription upload or a
  long synthesized response is stored whole.
- **Smaller audit rows**: entries carry a reference instead of the payload,
  which shrinks the database, its backups, and the audit list API.
- **One home for generated artifacts**: `/v1/videos` and `response_format:
  url` for images can store into the same service with a different `source`
  and TTL.
- **Mount-and-go storage**: a directory is enough; cloud object storage works
  today through a mount and natively once the S3 backend exists.

### Negative

- **A second thing to back up**: with the filesystem backend, the database
  and the media directory must be backed up and restored together. A restored
  database without its media directory renders placeholders.
- **Not shared across replicas by default**: several gateway instances need a
  shared mount (or the future S3 backend) to see each other's media. The
  memory backend is per-process.
- **Rows written before this change stay inline**: the dashboard keeps the
  base64 path for them; there is no migration of existing audit rows.

## Notes

This ADR covers the storage layer and the audit migration. It does not define:

- the `/v1/videos` endpoint, its provider adapters, or a pending-job sweep
- gateway-hosted URLs for generated images (`response_format: url`)
- signed, unauthenticated download URLs
- the S3 backend

Those build on `mediastore.Service` and are tracked in issue #1065.
