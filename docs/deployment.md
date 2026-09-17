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
anything larger than 256 KB; they expire within a day.

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
