---
title: Configuration
description: Every environment variable the relay reads.
---

The relay reads its settings from a `.env` file **and** from the process environment. The process
environment wins, so an orchestrator can override one value without editing the file. Every
problem is reported at startup in one list, and the relay refuses to start until they are fixed.

The annotated template is [`deploy/.env.example`](https://github.com/Mark7888/two-place-paste/blob/master/deploy/.env.example).
Sizes take a plain byte count or a unit: `500MB`, `5GiB`, `1.5GiB`.

## Required

| Variable | Description |
|---|---|
| `TPP_PUBLIC_BASE_URL` | The address devices use, scheme included and no trailing path: `https://tpp.example.com`. Creation links and their QR codes are built from it, and an `https://` value marks the admin cookie `Secure`. |
| `ADMIN_PASSWORD` | The admin page password. (`ADMIN_PASSWORD_HASH` is reserved for later; setting it stops the relay from starting.) |

## Server

| Variable | Default | Description |
|---|---|---|
| `TPP_HTTP_ADDR` | `:8080` | Listen address. Plain HTTP. |
| `TPP_HOST_PORT` | `8080` | Compose only: the host port the container is published on. |
| `TPP_HOST_BIND_ADDR` | `127.0.0.1` | Compose only: the host address the port is published on. Change it only when the proxy is on another machine, and name one private interface — never `0.0.0.0`. |
| `TPP_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. Even `debug` never logs clipboard content. |
| `TPP_ADMIN_SESSION_TTL` | `12h` | How long an admin sign-in lasts. |
| `TPP_API_READ_RATE`, `TPP_API_READ_BURST` | `10`, `60` | Each [admin API](/two-place-paste/self-hosting/api/) key's budget for reads (`GET`): requests a second, refilled continuously, and how many it may spend at once. |
| `TPP_API_WRITE_RATE`, `TPP_API_WRITE_BURST` | `2`, `10` | The same for every other request: creating, changing, purging and deleting. |
| `TPP_SOURCE_URL` | `https://github.com/Mark7888/two-place-paste` | Where the **Source code** link on the admin and pairing pages points. If you run a modified relay for other people, set it to your version's source — see [License](/two-place-paste/self-hosting/overview/#license). |
| `TPP_RELAY_OPERATOR_URL` | unset | Your website, such as `https://tpp.example.com/help`: who runs the relay and where people ask for more room. When set, the desktop app links to it as **Open operator website** — in its sidebar and in its out-of-space warnings — and so do the admin health card and the pairing page. Must be an absolute `http://` or `https://` URL. See [Operator website](#operator-website). |

### Operator website

The desktop app opens `TPP_RELAY_OPERATOR_URL` with the group's id added to its query, so
your site can tell which group a visitor came from:

```
https://tpp.example.com/help?group_id=<the group's id>
```

An existing query is kept (`…/help?lang=en&group_id=…`). The admin health card and the
pairing page link to the URL exactly as configured, without a group id: neither knows a
group. The group id is the creation token that made the group; it can create nothing
more and is no login, but it does identify the group, so point the variable only at a
site you trust with that.

## Redis

| Variable | Default | Description |
|---|---|---|
| `TPP_REDIS_ADDR` | `127.0.0.1:6379` | Redis address. In the compose file: `redis:6379`. |
| `TPP_REDIS_PASSWORD` | *(empty)* | Redis password, if any. |
| `TPP_REDIS_DB` | `0` | Redis database number. |

## Blob storage

| Variable | Default | Description |
|---|---|---|
| `TPP_BLOB_BACKEND` | `disk` | `disk` or `s3`. See [Blob storage](/two-place-paste/self-hosting/blob-storage/). |
| `TPP_BLOB_ROOT` | `/var/lib/tpp/blobs` | Where the disk backend writes. |
| `TPP_BLOB_SWEEP_INTERVAL` | `10m` | How often expired blobs are removed, after the pass at startup. |

## S3 (with `TPP_BLOB_BACKEND=s3`)

| Variable | Default | Description |
|---|---|---|
| `TPP_S3_ENDPOINT` | *(empty = AWS)* | `host[:port]`, optionally with `http://` or `https://`. |
| `TPP_S3_USE_SSL` | `true` | `false` for an `http://` endpoint. |
| `TPP_S3_REGION` | `us-east-1` | Region. |
| `TPP_S3_BUCKET` | — | Required. |
| `TPP_S3_PREFIX` | *(empty)* | Key prefix, so a bucket can be shared. |
| `TPP_S3_ACCESS_KEY_ID`, `TPP_S3_SECRET_ACCESS_KEY` | *(empty)* | Static credentials. Both empty uses the AWS credential chain. Each has a `_FILE` variant that reads it from a file (Docker secrets). |
| `TPP_S3_PATH_STYLE` | `auto` | `auto`, `path` or `virtual`. Auto is path-style for a custom endpoint, virtual-host for AWS. |
| `TPP_S3_CREATE_BUCKET` | `false` | Create the bucket at startup if it is missing. |

## Storage limits

| Variable | Default | Description |
|---|---|---|
| `TPP_BLOB_MAX_TOTAL_BYTES` | `0` (unlimited) | All blob storage. Set it a little under the volume's size. At least `TPP_ENTRY_MAX_BYTES`. |
| `TPP_BLOB_MAX_GROUP_BYTES` | `1GiB` | Per group, so one busy group cannot push out everyone else's entries. At least `TPP_ENTRY_MAX_BYTES`. |
| `TPP_BLOB_MIN_FREE_BYTES` | `0` (off) | Disk backend: refuse new blobs while the volume has less than this free. |

When a new entry would not fit, the oldest entries are removed to make room — see
[Storage limits and eviction](/two-place-paste/self-hosting/blob-storage/#storage-limits-and-eviction).

## Entry limits

These are sizes of **ciphertext**; the relay never knows the plaintext size.

| Variable | Default | Description |
|---|---|---|
| `TPP_ENTRY_INLINE_MAX_BYTES` | `256KiB` | Entries up to this size are kept in Redis; larger ones go to the blob store. |
| `TPP_ENTRY_MAX_BYTES` | `50MiB` | Hard cap per entry. |
| `TPP_ENTRY_CHUNK_BYTES` | `8MiB` | Chunk size for entries over 10 MiB. At most 10 MiB; with S3 at least 5 MiB. |
| `TPP_UPLOAD_SESSION_TTL` | `10m` | How long an unfinished chunked upload may sit idle (1m–1h). |

## Store limits

The store keeps items a group saves on purpose, beside its 24-hour history. It is **off for
every group** until you turn it on from the group's page in the [admin](/two-place-paste/self-hosting/admin/#the-store).
Its limits are separate from the blob limits above, and **the store never evicts**: a save that
does not fit is refused, and the app says how much room is left. Sizes count each item's
ciphertext plus about 2 KiB of fixed record.

| Variable | Default | Description |
|---|---|---|
| `TPP_STORE_MAX_TOTAL_BYTES` | `0` (unlimited) | Every group's store together. At least one item of `TPP_STORE_MAX_ITEM_BYTES` plus its record (2194 bytes). |
| `TPP_STORE_MAX_GROUP_BYTES` | `100MiB` | One group's store, unless its tag or the group itself overrides it. `0` is unlimited. At least one item plus its record, like the total. |
| `TPP_STORE_MAX_ITEM_BYTES` | `TPP_ENTRY_MAX_BYTES` | The largest item, so anything that fits in history can be saved. Unset, it is cut down to what the store limits above can hold; set, the relay refuses to start if a store limit cannot hold one such item. |
| `TPP_STORE_MAX_GROUP_ITEMS` | `1000` | Items per group. Each one is a record of about 2 KiB in Redis. |

No limit can be smaller than the largest thing it has to hold: the relay refuses to start
with one, and the admin screens refuse a group or tag limit below one entry (or, for a store,
one item and its record). See [Tags and storage limits](/two-place-paste/self-hosting/admin/#tags-and-storage-limits).
