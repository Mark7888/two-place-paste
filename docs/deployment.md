# Deploying the TwoPlacePaste server

**Spec:** SPEC §4.1, §4.2, §4.4, §4.5 · **Files:** `/deploy/**`

The server is one static Go binary and one Redis instance. It speaks plain
HTTP and expects **TLS to be terminated by a reverse proxy in front of it**
(SPEC §4.1). That is not a convenience: the admin session cookie is marked
`Secure` whenever `TPP_PUBLIC_BASE_URL` is an `https://` URL, and the clients
build their WebSocket URL from the same origin.

---

## 1. What is running

| Container | Image | Purpose |
|---|---|---|
| `server` | built from `deploy/Dockerfile` | relay (`/ws`), admin UI (`/admin/`), creation endpoint (`/<token>`), health (`/healthz`) |
| `redis` | `redis:7-alpine` + `deploy/redis.conf` | the only datastore (SPEC §4.2) |

Two named volumes hold everything that must survive:

| Volume | Holds | Losing it costs |
|---|---|---|
| `redis-data` | groups, devices, public keys, wrapped group keys, epochs, creation tokens | **the groups themselves** — permanently |
| `blob-data` | ciphertext of entries larger than 256 KB | at most 24 hours of clipboard history |

The server container runs as uid 10001 with a read-only root filesystem, no
capabilities and `no-new-privileges`. The only writable path is the blob
volume.

## 2. First run

```sh
cd deploy
cp .env.example .env
$EDITOR .env          # set TPP_PUBLIC_BASE_URL and ADMIN_PASSWORD
docker compose up -d --build
curl -fsS http://127.0.0.1:8080/healthz     # -> ok
```

The published port is bound to `127.0.0.1` on purpose: the reverse proxy is
the only thing that should reach the server.

`TPP_HOST_BIND_ADDR` moves that binding when the proxy is not on this host's
loopback — a proxy on another machine over a private network, or a host with
several addresses where only one should carry this traffic:

```sh
TPP_HOST_BIND_ADDR=10.0.0.4      # the interface the proxy reaches, and no other
```

Name one interface. The server speaks plain HTTP (SPEC §4.1), so an address the
internet can reach publishes clipboard traffic with no TLS in front of it, and
`0.0.0.0` is every interface at once.

Every variable is documented in `deploy/.env.example`. Two of them are
required and have no default:

- `TPP_PUBLIC_BASE_URL` — the externally reachable origin. Creation URLs and
  their QR codes are built from it (SPEC §3.1, §4.4), so a wrong value hands
  users a URL that does not resolve.
- `ADMIN_PASSWORD` — the admin UI credential. `ADMIN_PASSWORD_HASH` is
  accepted by the config loader so that moving to hashed storage stays a config
  change, but verifying a hash is deferred work (SPEC §9): set it and the
  server refuses to start rather than pretending to check it.

## 3. Reverse proxy

The proxy terminates TLS, forwards to the loopback port, and must pass the
WebSocket upgrade through. It should also set `X-Forwarded-For`: the login
rate limiter charges the address in its rightmost entry when the immediate
peer is loopback or a private address, and falls back to the peer otherwise.
Without it every request looks like it comes from the proxy and per-IP
limiting collapses into a single bucket (the global cap still applies).

**Caddy**

```caddyfile
tpp.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

Caddy sets `X-Forwarded-For` and handles the upgrade with no extra
configuration.

**nginx**

```nginx
server {
    listen 443 ssl http2;
    server_name tpp.example.com;

    ssl_certificate     /etc/letsencrypt/live/tpp.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/tpp.example.com/privkey.pem;

    # 10 MB ciphertext cap (SPEC §4.3), plus room for framing.
    client_max_body_size 12m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade    $http_upgrade;   # required for /ws
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host              $host;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # A WebSocket is idle between clipboard events; do not sever it.
        proxy_read_timeout  1h;
        proxy_send_timeout  1h;
    }
}

map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
```

Do not put a caching layer in front of the server. `/admin/**` and the
creation endpoint send `Cache-Control: no-store`, and a token's QR code is a
live credential.

## 4. Handing out a group

1. Sign in at `https://tpp.example.com/admin/`.
2. Generate a named creation token. The screen shows its URL and a QR code.
3. Send the URL — or let the user scan the code — and they select **New
   Group** in a client.

`GET` on a creation URL returns **404** (SPEC §3.1). That is deliberate: a
crawler, a link preview or a mail scanner following the link cannot burn the
token, because only the client's `POST` consumes it. One token creates exactly
one group; a second attempt gets `409`.

A token handed out by mistake, or one that was never used, can be **revoked**
from the same screen. Its URL then returns 404 like any unknown token. A used
token cannot be revoked — its group already exists; remove that group's devices
by revoking them from a client.

The admin screen lists tokens and nothing else. There is no content access to
add later — the server holds only ciphertext (SPEC §2.3).

## 5. Redis settings that are not tuning

`deploy/redis.conf` sets two things the deployment cannot do without:

- `appendonly yes` — the persistent zone must survive a restart. With RDB
  snapshots alone, every group created since the last save is lost, and a lost
  group is unrecoverable: the server never had the group key in the clear.
- `maxmemory-policy volatile-lru` — only keys with a TTL may be evicted, which
  is exactly the ephemeral zone. **`allkeys-lru` would silently evict group
  records, device records and wrapped keys**, destroying groups and pairings
  under memory pressure with no error anywhere (SPEC §4.2).

Size `maxmemory` for the ephemeral zone. Entries are capped at 10 MB of
ciphertext and only those at or below 256 KB are stored in Redis at all
(SPEC §4.3).

## 6. Blob storage and garbage collection

Blobs live at `<TPP_BLOB_ROOT>/<YYYYMMDDHH>/<entry_id>.bin`, where the
directory is the UTC hour in which the blob expires. Reclamation lists the
top-level directories and removes the ones fully in the past — no Redis query,
no per-file stat, cost proportional to hours rather than to blobs (SPEC §4.5).

The sweep runs once at startup and then every `TPP_BLOB_SWEEP_INTERVAL`. The
startup pass is what makes downtime harmless: a server that was off for a week
clears the week's expired buckets in its first listing.

After Redis data loss, a restore from backup, or a migration:

```sh
docker compose exec server tpp gc --verify
```

It cross-checks stored blobs against entry records and **deletes nothing**.
Two kinds of finding:

- *orphans* — blobs no entry references. Some are normal: an entry whose Redis
  record expired in the last hour still has its blob until the sweep reaches
  the bucket.
- *dangling entries* — entries whose blob is missing. These are the ones that
  matter: the entry is listed and unreadable.

It also compares the storage accounting with the entry records (below). A
small difference while uploads are in progress is normal.

### S3 and MinIO

`TPP_BLOB_BACKEND=s3` stores blobs in an S3-compatible object store instead of
the `blob-data` volume, under the same `<YYYYMMDDHH>/<entry_id>.bin` layout
(after `TPP_S3_PREFIX`, if set). The sweep, the storage limits and
`tpp gc --verify` work the same on both backends. Every byte goes through the
relay: the object store is never exposed to clients, so it belongs on a private
network.

At startup the server checks that the bucket exists — or creates it, with
`TPP_S3_CREATE_BUCKET=true` — and writes, reads and deletes a probe object under
`.tpp-probe/`. Any failure stops the start with the reason. There is no
fallback to disk.

**MinIO with compose.** `deploy/docker-compose.yml` has a `minio` service behind
the `minio` profile. Its root credentials are `TPP_S3_ACCESS_KEY_ID` and
`TPP_S3_SECRET_ACCESS_KEY` from `.env` (MinIO needs the secret to be at least 8
characters), so one pair configures both sides:

```sh
TPP_BLOB_BACKEND=s3
TPP_S3_ENDPOINT=http://minio:9000
TPP_S3_BUCKET=tpp
TPP_S3_ACCESS_KEY_ID=tpp
TPP_S3_SECRET_ACCESS_KEY=change-me-to-at-least-8-chars
TPP_S3_CREATE_BUCKET=true
```

```sh
docker compose --profile minio up -d --build
```

Its data is the `minio-data` volume, which holds at most a day of ciphertext,
like `blob-data`. MinIO no longer publishes new community images, so the
service uses `minio/minio:latest` by default; set `TPP_MINIO_IMAGE` in `.env` to
pin a release or to use an image you build or mirror yourself.

To give the relay a key scoped to its bucket rather than the root pair, create a
user and policy in MinIO, set `TPP_S3_ACCESS_KEY_ID` / `TPP_S3_SECRET_ACCESS_KEY`
to that user, and set `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` for the `minio`
service separately in an override file.

**AWS S3.** Leave `TPP_S3_ENDPOINT` empty and set `TPP_S3_REGION` and
`TPP_S3_BUCKET`. With both key variables empty the server uses the AWS
credential chain (`AWS_*` environment variables, then an instance or task
role). The relay needs this policy on its bucket and nothing else:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["s3:ListBucket"],
      "Resource": "arn:aws:s3:::my-tpp-blobs"
    },
    {
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"],
      "Resource": "arn:aws:s3:::my-tpp-blobs/*"
    }
  ]
}
```

Add `s3:CreateBucket` only if you use `TPP_S3_CREATE_BUCKET=true`. A lifecycle
rule that expires objects after 2 days is a good backstop for a relay that stays
down; the server's own sweep deletes expired objects hourly.

**Switching backends.** Change `TPP_BLOB_BACKEND` and restart. Entries already
written keep pointing at the old backend and read as gone within a day; nothing
needs migrating, because nothing lives longer than that.

### Storage limits and eviction

`TPP_BLOB_MAX_TOTAL_BYTES` caps all blob storage and `TPP_BLOB_MAX_GROUP_BYTES`
(1 GiB by default) caps each group. Only blobs count; inline entries live in
Redis and are bounded by its `maxmemory`.

When a new entry would not fit, the server evicts the **oldest** entries to
make room, even ones inside their 24 hours: first the writing group's own,
down to its group limit, then the oldest anywhere, down to the total limit. An
evicted entry is deleted — record, then blob — and every online device of its
group is told, so its history updates; to anyone else it reads as gone, like
an expired entry. Each eviction is logged at `info` with the group, entry id
and size. If evictions are frequent, the limits are too tight for how the
relay is used.

A write is refused with `QUOTA_EXCEEDED` only when the room it needs is held by
uploads still in progress, and with `TOO_LARGE` when the entry alone is larger
than a limit. The disk backend also refuses writes while its volume has less
than `TPP_BLOB_MIN_FREE_BYTES` free, which protects the host from something
else filling the disk.

The accounting lives in Redis under `quota:*` keys with no TTL. Every
accounted entry carries its expiry and is forgotten at the next sweep after
it, so the accounting corrects itself within a day of any crash. After
restoring Redis from a backup, recompute it at once instead:

```sh
docker compose exec server tpp gc --rebuild-accounting
```

It reads the entry records and replaces the accounting; it deletes no blob.
Uploads in flight while it runs are not counted until they expire, so run it
when the relay is quiet.

## 7. Backup and restore

Back up both volumes. Redis is the one that cannot be rebuilt.

```sh
# Redis: take a consistent snapshot, then copy the data directory.
docker compose exec redis redis-cli BGREWRITEAOF
docker run --rm -v tpp_redis-data:/data -v "$PWD":/backup alpine \
    tar czf /backup/redis-$(date -u +%Y%m%d).tar.gz -C /data .

# Blobs: at most 24h of ciphertext, so this is optional.
docker run --rm -v tpp_blob-data:/blobs -v "$PWD":/backup alpine \
    tar czf /backup/blobs-$(date -u +%Y%m%d).tar.gz -C /blobs .
```

Restore is the same in reverse, into a stopped stack. After restoring Redis
without its blobs, run `tpp gc --verify` and expect dangling entries for
anything larger than 256 KB; they expire within a day. Then run
`tpp gc --rebuild-accounting` so the storage limits count what is really
stored.

## 8. Upgrades and operations

```sh
docker compose pull && docker compose up -d --build   # rolling restart
docker compose logs -f server                          # JSON, one object per line
```

A restart is cheap and loses nothing but the WebSocket connections, which
clients re-establish with backoff, and admin sessions, which are held in
memory. Shutdown is graceful: the server stops accepting connections, gives
in-flight requests 15 seconds, and exits.

Logs are JSON on stderr at `TPP_LOG_LEVEL`. They contain group ids, device
ids, epochs, sizes and timestamps — and by design never a clipboard body, a
content type, a filename, a key, a wrapped key, a creation token, a pairing
token or a session cookie (SPEC §2.3).
