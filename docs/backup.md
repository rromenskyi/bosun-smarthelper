# Backing up

`smarthelper backup` archives the persistent data directory (memos,
uploaded documents and their images, sessions, settings, metric-merge
suggestions, the error log, and the monitoring dashboard's metrics) and
uploads it to any S3-compatible bucket — AWS S3, Backblaze B2, MinIO,
Wasabi, or anything else that speaks the same API.

## Why manual, not scheduled

There's deliberately no cron/timer variant. A backup means reading and
uploading everything in the data directory — on a connection that isn't
always fast or unmetered (a boat/RV on Starlink, say), that's real
bandwidth you don't want spent automatically on someone else's schedule.
Run it by hand, when it's actually a good time to:

```bash
smarthelper backup
```

## Config

```yaml
backup:
  s3:
    endpoint: "https://s3.us-west-002.backblazeb2.com"
    region: "us-west-002"
    bucket: "bosun-backups"
    access_key_id_env: "BACKUP_S3_ACCESS_KEY_ID"
    secret_access_key_env: "BACKUP_S3_SECRET_ACCESS_KEY"
```

Credentials are env var *names* (`.env`), the same indirection
`llm.remote.api_key_env` already uses — never written into `config.yaml`
itself. `backup.data_dir` is optional; empty uses the same default
directory every store (memos, documents, ...) already falls back to on
its own.

Each run uploads one new object, `bosun-backup-<UTC timestamp>.tar.gz` —
nothing is overwritten or pruned automatically; that's a bucket lifecycle
policy question for whichever provider you use, not something this
command manages.

## No AWS SDK

Uploading is a single PUT request, signed with AWS Signature Version 4
(`internal/backup/s3.go`) by hand — no SDK dependency. The SDK is built to
cover the entire S3 API surface (multipart uploads, pagination, dozens of
services sharing the same signing code); this project needs exactly one
operation on one already-fully-buffered payload, and SigV4 for that case
is a short, self-contained, well-documented algorithm — pulling in a
dependency tree many times the size of the feature it would serve wasn't
worth it for what's ultimately a `hmac.New(sha256.New, ...)` chain.

Verified against a real server, not just checked against a remembered
reference signature: a local MinIO container, `PutObject` against it, and
`mc cat` confirming the uploaded object's bytes matched exactly.
`internal/backup/s3_test.go`'s own tests use `httptest` instead (no Docker
dependency for `go test ./...` itself) to check the same request shape —
method, path-style URL, `Authorization` header format, payload hash — a
real server isn't needed to catch a regression in how the request is
built, only to have first confirmed the algorithm itself is correct.

## Why metrics.db becomes metrics.sql

The monitoring dashboard's data (`internal/metrics`) lives in a SQLite
file. Copying it as a raw file risks a torn, inconsistent snapshot if a
write lands mid-copy — `internal/metrics.Collector` writes to it every
`metrics.interval` (default 30s). `internal/backup.DumpSQL` instead reads
it through the same transactional API any other query would and renders
every table as portable `CREATE TABLE`/`INSERT` SQL text, which is also
restorable across SQLite versions without needing the exact same on-disk
page format. Schema-agnostic — it walks `sqlite_master` rather than
assuming the current one-table shape, so it keeps working if that schema
ever grows.

## Restoring

```bash
tar xzf bosun-backup-2026-01-01T00-00-00Z.tar.gz -C /path/to/restore
sqlite3 /path/to/restore/metrics.db < /path/to/restore/metrics.sql
```

(`metrics.sql` itself isn't picked up by `internal/metrics.Store` — replay
it into a fresh `metrics.db` first, as above.)
