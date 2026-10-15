# Plan — S3 blob storage, chunked transfer, storage quota and admin dashboard

**Status:** in progress. Done: step 1 (token revocation) and step 2 (storage limits,
accounting and eviction on the disk backend, `EntryEvicted`, `QUOTA_EXCEEDED`,
`tpp gc --rebuild-accounting`) and step 3 (the S3 backend, the MinIO compose profile, and
Redis and MinIO in CI), step 4 (the admin dashboard) and step 5 (chunked transfer on the
server: `EntryLimits`, `EntryUpload{Begin,Chunk,Abort}`, commit by `upload_handle`,
`EntryFetchChunk`; 50 MiB cap, 8 MiB chunks, upload sessions in memory). Next: step 6,
the clients.
**Touches:** `/proto/**` (P1), `/server/**` (P3), `/pkg/tppclient/**` (P5),
`/desktop/**` (P6), `/mobile/**` (P7), `/deploy/**`, `/docs/**`.

---

## 1. Goals

- Store large entries (images, files, long text) in **S3-compatible object storage**:
  MinIO first, AWS S3 later, with no code change between the two.
- Keep **local disk** as the default backend, so a fresh deployment needs nothing beyond
  Redis.
- Raise the per-entry cap from 10 MB to a configurable **50 MB**, carried in **chunks of
  at most 8 MiB**: no WebSocket frame above 10 MB, ever.
- Bound total storage: a **global limit** (e.g. 5 GiB) and a **per-group limit** (default
  1 GiB). When an upload would not fit, the **oldest entries are evicted**, even if
  they are still inside their 24 hours.
- An **admin dashboard** with usage data, and **revoking** an unused creation token.

## 2. What exists today

| Piece | Where | State |
|---|---|---|
| `blob.Backend` (`Put`/`Get`/`Delete`/`Sweep`) | `server/internal/blob/blob.go` | Done |
| Disk backend, hour-bucket layout, sweeper | `server/internal/blob/disk.go`, `sweeper.go` | Done |
| S3 backend | `server/internal/blob/s3.go` | Stub, every call returns `ErrNotImplemented` |
| `TPP_BLOB_BACKEND=s3` | `server/cmd/tpp/main.go` (`newBlobBackend`) | Refused at startup |
| `EntryPutRequest.upload_handle` | `proto/tpp/v1/entry.proto` | Reserved for exactly this |
| 10 MB cap | server config, `pkg/tppclient/entries.go`, `mobile/src/core/protocol/client.ts`, `ws/server.go` read limit | Hard-coded in clients |
| `tpp gc --verify` | `server/cmd/tpp/gc.go` | Disk only |
| Admin UI | `server/internal/admin`, `server/web/admin` | Token list + create only |

The entry crypto (`/spec/crypto.md` §5) is one XChaCha20-Poly1305 seal over the whole
container. Chunking is a **transport** concern only: the client still seals and opens
the whole entry in memory, and the crypto profile and test vectors do not change.

## 3. Decisions

| # | Decision | Why |
|---|---|---|
| D1 | Backend is chosen explicitly: `TPP_BLOB_BACKEND=disk` (default) or `s3`. With `s3`, startup verifies the bucket with a probe write/read/delete and **refuses to start** on failure. No runtime fallback to disk. | Predictable; one place to account and sweep. |
| D2 | All bytes go **through the relay**. No presigned URLs. | MinIO stays on the private compose network; no public TLS/CORS for it; disk and S3 behave identically for clients. |
| D3 | Per-entry cap 50 MiB (configurable), chunk size 8 MiB. | Under the 10 MB frame limit, above S3's 5 MiB minimum multipart part size, so one chunk = one S3 part. |
| D4 | Global limit + per-group limit (default 1 GiB). Only **blob** storage counts; inline entries are bounded by Redis `maxmemory`. | Stops one group from evicting everyone else. |
| D5 | Eviction = early deletion of the entry **and** its blob. | SPEC §4.5 prohibits *extending* a lifetime, not shortening one. Early deletion is already allowed there. |
| D6 | The S3 `Sweep` **actively deletes** expired hour prefixes. A bucket lifecycle rule is an optional backstop. | Lifecycle rules work in whole days; the quota would count dead objects for up to a day. Requires a SPEC §4.5 edit. |
| D7 | SDK: `github.com/minio/minio-go/v7`. | One module, speaks to MinIO and AWS alike, far smaller dependency tree than `aws-sdk-go-v2` (conventions §9). |
| D8 | The relay pushes an `EntryEvicted` event so open History lists update at once. | Agreed. |
| D9 | The admin UI gets a dashboard of **byte counts only**: no content, no filenames, no content types. | Keeps SPEC §4.4's "no content access, by construction". |
| D10 | "Revoke an invite" applies to **unused** creation tokens: the token is deleted and its URL stops working. A used token already produced a group; revoking it does nothing to the group. | A group is removed by revoking its devices, not by the admin. |

## 4. Work items

### 4.1 Configuration — `server/internal/config`, `deploy/.env.example`

New keys, all validated in one pass like the rest:

| Key | Default | Notes |
|---|---|---|
| `TPP_BLOB_BACKEND` | `disk` | `disk` or `s3`. |
| `TPP_S3_ENDPOINT` | empty | Empty = AWS. `minio:9000` for the compose MinIO. |
| `TPP_S3_REGION` | `us-east-1` | |
| `TPP_S3_BUCKET` | — | Required with `s3`. |
| `TPP_S3_PREFIX` | empty | Key prefix, so a bucket can be shared. |
| `TPP_S3_ACCESS_KEY_ID` / `TPP_S3_SECRET_ACCESS_KEY` | empty | Both empty = standard AWS credential chain (env, IAM role). `_FILE` variants for Docker secrets. |
| `TPP_S3_USE_SSL` | `true` | `false` for an in-network MinIO. |
| `TPP_S3_PATH_STYLE` | `auto` | `auto` = path-style for a custom endpoint, virtual-host for AWS. |
| `TPP_S3_CREATE_BUCKET` | `false` | Create the bucket at startup if missing. |
| `TPP_ENTRY_MAX_BYTES` | `50MiB` | Per-entry ciphertext cap. |
| `TPP_ENTRY_CHUNK_BYTES` | `8MiB` | Must be < frame limit; ≥ 5 MiB with `s3`. |
| `TPP_BLOB_MAX_TOTAL_BYTES` | `0` (unlimited) | E.g. `5GiB`. |
| `TPP_BLOB_MAX_GROUP_BYTES` | `1GiB` | ≤ total limit when that is set. |
| `TPP_BLOB_MIN_FREE_BYTES` | `0` | Disk only: refuse uploads when the volume is nearly full, whatever the quota says. |
| `TPP_UPLOAD_SESSION_TTL` | `10m` | Abandoned uploads are aborted and their reservation released. |

- Byte sizes accept `5GiB`, `50MB` or a raw integer.
- Cross-checks: entry cap ≤ group limit ≤ total limit; chunk ≤ entry cap.
- Secrets are never logged; the startup line names backend, endpoint, bucket and limits.

### 4.2 Blob backend interface — `server/internal/blob/blob.go`

Add to `Backend`:

- `BeginUpload(ctx, entryID, expiresAt, size) (uploadID, error)`
- `WriteChunk(ctx, uploadID, index, r) error`
- `CompleteUpload(ctx, uploadID) (ref, error)`. Still create-only: `ErrExists` on a taken id.
- `AbortUpload(ctx, uploadID) error`
- `GetRange(ctx, ref, offset, length) (io.ReadCloser, error)`

Every backend also implements `Lister`, so `tpp gc --verify` works everywhere.

**Disk:** stage to `<root>/.uploads/<uploadID>`, then rename into the hour bucket (`os.Link` +
remove keeps the O_EXCL guarantee). The sweep removes staging files older than the
session TTL; `.uploads` is never parsed as a bucket.

A shared conformance suite (`blob/backendtest`) runs every implementation through the
same cases.

### 4.3 S3 backend — `server/internal/blob/s3.go`

- Key: `<prefix>/<YYYYMMDDHH>/<entry_id>.bin`, the same layout as disk.
- `Put`: `PutObject` with `If-None-Match: *` (create-only; supported by MinIO and AWS).
- Chunked upload: S3 multipart, one chunk per part.
- `Get` / `GetRange`: `GetObject` with an optional `Range`.
- `Sweep`: list the hour prefixes (`Delimiter: "/"`), batch-delete every expired one;
  abort multipart uploads older than the session TTL.
- `ListRefs`: paginated listing, for `gc --verify` and accounting rebuilds.
- Startup check: bucket exists (or is created), then a probe object is put, read and
  deleted. Any failure stops startup with an actionable message.

### 4.4 Storage accounting and quota — new `server/internal/quota`

Redis keys, **no TTL** (so `volatile-lru` cannot evict them):

```
blobs:index                 zset   entry_id scored by expires_at_ms
blobs:meta                  hash   entry_id -> "<group_id>:<size>"
blobs:usage                 string global committed bytes
blobs:reserved              string global bytes held by in-flight uploads
group:<group_id>:blob_usage string per-group committed bytes
upload:<upload_handle>      hash   group, device, entry_id, size, next_index   [TTL = session TTL]
```

- **`Reserve(group, size)`**, one Lua script, called at upload start (and for a
  single-frame blob `Put`):
  1. `size` > group limit → `TOO_LARGE`.
  2. Group's oldest entries become eviction victims until it fits the group limit.
  3. Globally oldest entries become victims until it fits the global limit.
  4. In-flight reservations count as used. If eviction cannot free enough space
     (only reservations remain), refuse with `QUOTA_EXCEEDED`.
  5. The victim list and the reservation are returned atomically.
- **Evict**: remove `entry:<id>`, `entry:<id>:blob` and the group's history member first,
  then `Backend.Delete`, then push `EntryEvicted` to the group. A reader racing it gets
  `NOT_FOUND`, exactly as with expiry.
- **Commit** moves the reservation into usage. **Abort or timeout** releases it.
- **Expiry**: each sweep also runs a Lua script that removes `blobs:index` members with
  score < now and decrements the counters.
- **Drift**: `tpp gc --verify` reports counters vs. the index vs. the backend listing.
  `tpp gc --rebuild-accounting` recomputes the counters from those. It is the only
  writing gc mode, and it still never deletes a blob.

### 4.5 Chunked transfer protocol — `proto/tpp/v1`

New messages (numbers permanent, per `error.proto`'s rule):

```
EntryUploadBeginRequest  { entry_id, epoch, size }   -> EntryUploadBeginResponse { upload_handle, chunk_size }
EntryUploadChunkRequest  { upload_handle, index, data } -> EntryUploadChunkResponse { received_bytes }
EntryPutRequest          { entry_id, epoch, size, upload_handle }  // commit, existing message
EntryUploadAbortRequest  { upload_handle }
EntryFetchChunkRequest   { entry_id, offset, length } -> EntryFetchChunkResponse { data }
EntryEvicted (event)     { entry_id }
ErrorCode QUOTA_EXCEEDED
```

- Limits (max entry, chunk size, single-frame max) are advertised in the server's
  connection hello, so clients stop hard-coding 10 MB.
- Chunks are sent in order, one in flight; an out-of-order index is `INVALID_ARGUMENT`.
- An upload handle is bound to its device and group; any other connection gets
  `NOT_FOUND`.
- **Compatibility:** entries that fit one frame keep using the current single-frame
  path, so clients that predate this change keep working.

### 4.6 Server wiring — `entries`, `ws`, `cmd/tpp`

- `entries.Service`: `BeginUpload`, `WriteChunk`, `CommitUpload`, `AbortUpload`,
  `FetchRange`; quota reservation in `Put` and `BeginUpload`.
- `ws`: handlers for the new messages; read limit becomes `chunk size + overhead`;
  `EntryEvicted` fan-out through the hub.
- `cmd/tpp`: `newBlobBackend` builds S3; the sweeper also prunes accounting and stale
  uploads; `gc --verify` and `gc --rebuild-accounting` work for both backends.

### 4.7 Admin UI — `server/internal/admin`, `server/web/admin`

**Dashboard** (`GET /admin/`, with tokens moved to `GET /admin/tokens`):

- Storage: global usage vs. limit (bar), reserved bytes, blob count, backend and bucket.
- Per group: group name (from its creation token), device count, entry count, blob
  bytes vs. group limit, oldest entry age. Sorted by usage.
- Recent evictions: count in the last 24 h (counter in Redis), so an operator can see
  when limits are too tight.
- Backend health: last sweep time and result, last S3 error if any.
- Byte counts only: no entry ids, content types or filenames are shown.

**Revoke an invite:**

- `POST /admin/tokens/{token}/revoke`, with CSRF protection like the existing POSTs,
  and a confirm step in the UI.
- `store.RevokeToken`: atomic Lua script that deletes an **unused** token; a used one
  returns `ErrTokenConsumed` and the UI says so. After revocation, `POST /{token}` returns
  the same 404 as an unknown token.
- The token list shows a Revoke button on unused tokens only.

### 4.8 Clients

- `pkg/tppclient`: read limits from the hello; chunked upload/download with a progress
  callback; abort on cancel; map `QUOTA_EXCEEDED`; handle `EntryEvicted`; frame read
  limit = chunk + overhead.
- Desktop UI: progress for large transfers; clear messages for "too large" and "storage
  full"; History drops evicted entries live.
- Mobile (`mobile/src/core/protocol/client.ts`): same; check memory with 50 MB entries
  on a low-end Android device.
- Interop tests (`mobile/src/interop`) cover a chunked round trip between Go and TS.

### 4.9 Deployment — `deploy/`, `docs/deployment.md`

- `docker-compose.yml`: an optional `minio` service behind a compose profile
  (`docker compose --profile minio up -d`), on the private network only, plus a one-shot
  `mc` init container that creates the bucket and a bucket-scoped access key.
- `.env.example`: a commented, copy-paste MinIO block and an AWS block.
- `docs/deployment.md`: MinIO setup; AWS least-privilege IAM policy (Put/Get/Delete/List
  and multipart on one bucket); optional lifecycle backstop rule; switching disk → S3
  (switch and let the old disk blobs age out within 24 h); sizing the limits.

### 4.10 Specs and docs

- SPECS §4.3: configurable cap, chunked transfer, quota.
- SPECS §4.4: dashboard scope (byte counts only), token revocation.
- SPECS §4.5: eviction as early deletion, active S3 sweep, accounting.
- ROADMAP: move "S3 blob backend" out of the deferred backlog into a new phase.
- README: configuration table and the "10 MB per entry" line.
- conventions §9 / `server/go.mod` comment: reason for `minio-go`.

### 4.11 Tests and CI

- Quota Lua scripts against real Redis: eviction order, group vs. global limit,
  reservations under concurrency, expiry pruning, rebuild.
- Backend conformance suite against disk, and against **MinIO in CI** (a `go.yml`
  service container, `integration` build tag).
- WebSocket end to end: 50 MB chunked round trip, abort, expired session, chunk from
  another device, quota exhausted mid-upload, `EntryEvicted` delivery.
- Admin: dashboard renders with no content-bearing fields; revoke of an unused and of a
  used token; CSRF on revoke.

## 5. Order of work

Each step ships on its own and keeps existing clients working until step 6.

1. Token revocation (4.7, second half). Independent of everything else.
2. Config (4.1) + quota/accounting (4.4) on the disk backend.
3. S3 backend (4.3) + MinIO compose profile (4.9).
4. Admin dashboard (4.7, first half).
5. Chunked protocol (4.2 multipart methods, 4.5, 4.6).
6. Clients (4.8).

Spec, README and deployment docs (4.10) are updated alongside each step.
