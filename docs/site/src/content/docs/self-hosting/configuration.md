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
| `TPP_BLOB_MAX_TOTAL_BYTES` | `0` (unlimited) | All blob storage. Set it a little under the volume's size. |
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
