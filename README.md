[![Go](https://github.com/Mark7888/two-place-paste/actions/workflows/go.yml/badge.svg)](https://github.com/Mark7888/two-place-paste/actions/workflows/go.yml)
[![Desktop](https://github.com/Mark7888/two-place-paste/actions/workflows/desktop.yml/badge.svg)](https://github.com/Mark7888/two-place-paste/actions/workflows/desktop.yml)
[![Android](https://github.com/Mark7888/two-place-paste/actions/workflows/android.yml/badge.svg)](https://github.com/Mark7888/two-place-paste/actions/workflows/android.yml)

# TwoPlacePaste

Share your clipboard across devices.

A self-hosted, end-to-end encrypted shared clipboard. Copy on one device, paste
on another. The server relays ciphertext and never sees clipboard content.

---

## What you get

| Component | Stack | State |
|---|---|---|
| Relay server (`tpp`) | Go, Redis | Works. Relay, group/device registry, admin UI, blob GC. |
| Desktop service (`tppdesktop`) | Go + embedded React UI | Works on Windows and macOS. Tray icon, localhost web UI. |
| Go client core (`pkg/tppclient`) | Go | Works. All crypto and protocol; the desktop app is a shell around it. |
| Android app | React Native | In development — not documented here. |

A Linux desktop build compiles and runs, but has no clipboard, no tray and no
login item (it prints its UI URL to stderr instead); the Linux desktop is
deferred work, not a supported target.

**How it is secured.** Each group has one symmetric key shared by its devices.
The server only ever holds that key wrapped to each device's public key, and
only ever stores ciphertext. Revoking a device rotates the key (a new *epoch*),
so the revoked device cannot read anything written afterwards. Every entry
expires after 24 hours — there is no pin, favourite or extend-TTL feature, and
there will not be one: the blob garbage collector depends on immutable
lifetimes.

Full design: [`docs/SPECS.md`](docs/SPECS.md). Crypto profile: `spec/crypto.md`.

---

## 1. Set up the relay server

You need one host with Docker and a DNS name pointing at it. TLS is **not**
terminated by the server — put a reverse proxy in front of it.

### With the published image

The server is published to the GitHub Container Registry as
`ghcr.io/mark7888/two-place-paste-server`, for `linux/amd64` and `linux/arm64`.
No clone and no build are needed — only a `docker-compose.yml` and a `.env`:

```sh
mkdir tpp && cd tpp
curl -fsSL -o .env https://raw.githubusercontent.com/Mark7888/two-place-paste/master/deploy/.env.example
$EDITOR .env                  # set TPP_PUBLIC_BASE_URL and ADMIN_PASSWORD
$EDITOR docker-compose.yml    # paste the file below

docker compose up -d
curl -fsS http://127.0.0.1:8080/healthz      # -> ok
```

```yaml
# docker-compose.yml
name: tpp

services:
  redis:
    image: redis:7-alpine
    restart: unless-stopped
    # The settings of deploy/redis.conf. volatile-lru is not optional:
    # allkeys-lru would silently evict group records and destroy pairings.
    command: >-
      redis-server --appendonly yes --appendfsync everysec
      --maxmemory 512mb --maxmemory-policy volatile-lru --protected-mode no
    volumes:
      - redis-data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 10s
      timeout: 3s
      retries: 5
      start_period: 5s

  server:
    image: ghcr.io/mark7888/two-place-paste-server:latest
    restart: unless-stopped
    env_file: .env
    environment:
      TPP_REDIS_ADDR: redis:6379
      TPP_BLOB_ROOT: /var/lib/tpp/blobs
      TPP_HTTP_ADDR: ":8080"
    ports:
      # Loopback only: the reverse proxy is the one thing that should reach it.
      - "127.0.0.1:8080:8080"
    volumes:
      - blob-data:/var/lib/tpp/blobs
    user: "10001:10001"
    read_only: true
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    tmpfs:
      - /tmp
    depends_on:
      redis:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "wget", "--quiet", "--tries=1", "--spider", "http://127.0.0.1:8080/healthz"]
      interval: 30s
      timeout: 3s
      retries: 3
      start_period: 5s

volumes:
  redis-data:
  blob-data:
```

The image tags:

| Tag | What it is |
|---|---|
| `latest` | The newest full release. |
| `1.2.3`, `1.2`, `1` | A release, and the newest release of that minor and major version. Kept forever; pin one of these to upgrade only when you choose to. |
| `1.3.0-rc.1` | A pre-release. Kept forever, never `latest`. |
| `beta` | The newest build of the default branch — the server side of the apps' Beta channel. |
| `sha-abc1234` | The build of one commit (a default-branch push, a pull request or a release). Deleted a week after it was built. |

Upgrading is `docker compose pull && docker compose up -d`.

### From source

```sh
git clone https://github.com/Mark7888/two-place-paste
cd two-place-paste/deploy

cp .env.example .env
$EDITOR .env                  # set TPP_PUBLIC_BASE_URL and ADMIN_PASSWORD

docker compose up -d --build
curl -fsS http://127.0.0.1:8080/healthz      # -> ok
```

`deploy/docker-compose.yml` builds the server from `deploy/Dockerfile` rather
than pulling it, and reads Redis's settings from `deploy/redis.conf`.

Either way, that starts two containers: the server and Redis
(`redis:7-alpine`). The published port is bound to `127.0.0.1` on purpose —
only the reverse proxy should reach it.

### Required configuration

Every key is documented in [`deploy/.env.example`](deploy/.env.example); two
have no default:

| Variable | Why it is required |
|---|---|
| `TPP_PUBLIC_BASE_URL` | The externally reachable origin, e.g. `https://tpp.example.com`, scheme included, no trailing path. Group-creation URLs and their QR codes are built from it, and an `https://` value is what marks the admin cookie `Secure`. |
| `ADMIN_PASSWORD` | The admin UI credential. Set exactly one of `ADMIN_PASSWORD` and `ADMIN_PASSWORD_HASH`; the hash path is accepted by the config loader but not yet verified, so the server refuses to start if you set it. |

The rest have defaults: `TPP_HTTP_ADDR` (`:8080`), `TPP_LOG_LEVEL` (`info`),
`TPP_ADMIN_SESSION_TTL` (`12h`), `TPP_REDIS_ADDR` / `TPP_REDIS_PASSWORD` /
`TPP_REDIS_DB`, `TPP_BLOB_BACKEND` (`disk` or `s3`, below),
`TPP_BLOB_ROOT`, `TPP_BLOB_SWEEP_INTERVAL` (`10m`),
`TPP_ENTRY_INLINE_MAX_BYTES` (256 KiB — larger ciphertext goes to the blob
store), `TPP_ENTRY_MAX_BYTES` (50 MiB hard cap; anything over 10 MiB travels in
chunks of `TPP_ENTRY_CHUNK_BYTES`, 8 MiB) and `TPP_UPLOAD_SESSION_TTL` (`10m`, how
long an unfinished chunked upload may sit idle).

**Storage limits.** `TPP_BLOB_MAX_TOTAL_BYTES` (unlimited by default, e.g.
`5GiB`) and `TPP_BLOB_MAX_GROUP_BYTES` (`1GiB`) cap the blob store. When a new
entry would not fit, the oldest entries are evicted to make room — the writing
group's own first, then the oldest anywhere — even inside their 24 hours.
`TPP_BLOB_MIN_FREE_BYTES` additionally refuses writes when the disk itself runs
low. Sizes accept units (`500MB`, `5GiB`) or plain byte counts.

**S3 / MinIO.** Large entries can live in S3-compatible object storage instead of
on disk. To use the MinIO service that `deploy/docker-compose.yml` ships behind a
profile, set this in `.env` and start with `docker compose --profile minio up -d`:

```sh
TPP_BLOB_BACKEND=s3
TPP_S3_ENDPOINT=http://minio:9000
TPP_S3_BUCKET=tpp
TPP_S3_ACCESS_KEY_ID=tpp
TPP_S3_SECRET_ACCESS_KEY=change-me-to-at-least-8-chars   # also MinIO's root password
TPP_S3_CREATE_BUCKET=true
```

For AWS S3, leave `TPP_S3_ENDPOINT` empty and set `TPP_S3_REGION`. Every S3
setting is documented in `deploy/.env.example`; the server checks the bucket and
credentials at startup and refuses to start, with the reason, if either is wrong.

Configuration comes from `.env` **and** the process environment, and the
process environment wins. The loader validates eagerly and reports every
problem in one pass, so a bad `.env` fails at startup with the whole list.

### Reverse proxy

The proxy terminates TLS, forwards to the loopback port, passes the WebSocket
upgrade through, and should set `X-Forwarded-For` (the login rate limiter needs
it for per-IP buckets). Caddy needs nothing beyond:

```caddyfile
tpp.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

A worked nginx configuration — including the `Upgrade` headers and the long
read timeout an idle WebSocket needs — is in
[`docs/deployment.md`](docs/deployment.md). Do not put a cache in front of the
server.

### Server endpoints

| Path | Purpose |
|---|---|
| `/healthz` | Liveness. Returns `ok`. |
| `/ws` | The client WebSocket. |
| `/admin/` | Admin UI: a dashboard of storage and group usage, and creation tokens (generate and revoke). |
| `/<token>` | Group creation. `GET` returns 404 by design; only a client's `POST` spends the token. |

### Operating it

```sh
docker compose logs -f server                       # JSON, one object per line
docker compose pull && docker compose up -d         # upgrade (add --build from source)
docker compose exec server tpp gc --verify          # cross-check blobs; deletes nothing
docker compose exec server tpp gc --rebuild-accounting  # after a Redis restore
```

Back up the `redis-data` volume: it holds the groups, devices and wrapped keys,
and a lost group is unrecoverable because the server never had the group key in
the clear. The `blob-data` volume holds at most 24 hours of ciphertext.
Backup, restore, Redis tuning (`appendonly yes`, `maxmemory-policy
volatile-lru` — `allkeys-lru` would destroy groups) and the GC report format
are covered in [`docs/deployment.md`](docs/deployment.md).

---

## 2. Install the desktop app

Every [release](https://github.com/Mark7888/two-place-paste/releases/latest)
carries the installers. Once installed, the app updates itself (Settings →
Updates).

**macOS.** Run this in Terminal. It installs the latest release for your Mac
(Apple Silicon or Intel) into `/Applications`, or `~/Applications` when that is
not writable:

```sh
curl -fsSL https://github.com/Mark7888/two-place-paste/releases/latest/download/install-macos.sh | sh
```

The app is not notarised by Apple, and macOS asks for approval in System
Settings → Privacy & Security before it opens an app downloaded with a browser.
The script downloads with `curl`, which does not set that flag, so the app
opens straight away. It checks the download against the release's checksum.
Run the same command again to update by hand. To install a specific release or
the latest Beta build instead, set a variable in front of `sh`:

```sh
curl -fsSL https://github.com/Mark7888/two-place-paste/releases/latest/download/install-macos.sh | TPP_VERSION=0.1.0 sh
curl -fsSL https://github.com/Mark7888/two-place-paste/releases/latest/download/install-macos.sh | TPP_CHANNEL=beta sh
```

**Windows.** Download and run `TwoPlacePaste-Setup.exe`, a per-user installer
that needs no administrator rights.

**From source:**

```sh
cd desktop/ui && npm ci && npm run build   # builds into ../internal/localui/dist
cd .. && go build -o tppdesktop ./cmd/tppdesktop
```

The UI must be built **before** the Go build: the binary embeds
`internal/localui/dist`, and without it the service runs but serves a page
saying the UI was not built.

Running it opens `http://127.0.0.1:47821/app?token=<per-launch token>` in your
default browser. The token is minted per launch, is required on every request,
and is captured by the UI from the URL and then cleared from the address bar —
it is never stored in a cookie or in `localStorage`. The listener is
`127.0.0.1` only, and the `Origin` header is checked on every request including
the WebSocket upgrade.

The tray menu has **Sync now**, **Open TwoPlacePaste**, **Check for updates…**
and **Quit**. The tray icon shows the service's state: connecting (faded),
connected, uploading (↑), downloading (↓), both at once (⇅), disconnected
(struck through), or not paired yet (faded, empty). When an automatic upload
or download was held back by your size limit, or failed, the icon carries a
**!** badge until the next sync or until you dismiss the notice in the app. When **Sync now**
cannot tell which side is newer, it asks with a quiet notification that has an
**Upload this clipboard** and a **Download the latest** button. The same two
items appear in the tray menu for a few minutes, in case notifications are
turned off. On macOS the first notification asks for permission. If the port
is already taken, the service does not fail silently: the tray shows the bind
error instead and offers to open the settings file, which is where the port is
overridden.

Secrets — the device private key and the group key — live in the OS store: the
login keychain on macOS, a DPAPI-sealed file on Windows, and an owner-only
(optionally passphrase-encrypted) file elsewhere. The Sync tab shows which
backend is in use.

---

## 3. Use it

### Create a group

1. Sign in at `https://tpp.example.com/admin/` with `ADMIN_PASSWORD`.
2. Open **Tokens** and generate a named creation token. The screen shows its URL
   and a QR code.
3. On your first device, open the desktop app → **Pairing** → *Create a group*,
   paste the creation URL and press **Create**.

The device generates the group key locally and wraps it to itself; the relay
only ever receives the wrapped form. One token creates exactly one group — a
second attempt gets `409`. An unused token can be revoked from the admin screen,
after which its URL no longer works.

The admin **Dashboard** shows blob storage against its limits, evictions in the
last day, and per group the devices, last activity, entry count and storage
used — sizes and counts only, never content.

### Add a device

Pairing runs in both directions. Which one you want depends on which device has
the screen you are looking at — either way the code is a QR and the same string
as copyable text, so whichever form the other device can take, it is reading one
code.

**The member shows.** Use this when the joining device can scan, or can paste.

1. On a device already in the group: **Pairing** → **Show pairing code**. The
   token is single-use and expires in 5 minutes.
2. On the joining device: **Pairing** → *Join a group*, paste the payload,
   press **Join**.

The inviting device holds the group key, so it wraps it for the joiner as soon
as the relay reports the join. Keep the inviting device open until that happens.

**The joining device shows.** Use this when the *member* is the one that can
read a code — a phone scanning a desktop that is not in the group yet, or a
desktop pasting a code from a phone that is not.

1. On the joining device: **Pairing** → *Show a code*. It asks for the relay's
   address once, because a device with no group does not know one yet, and the
   code carries it.
2. On a device already in the group: **Pairing** → *Read the code*, then check
   the dialog. It names the joining device and shows a fingerprint of its public
   key; confirm only if that fingerprint matches the one that device is showing.

That confirmation is not a formality. In this direction it is the **member** who
acts, and adding a device hands it the group key, so nothing is wrapped until
you say yes. Keep the joining device open until you do.

### Sync the clipboard

The **Sync** tab gives you three actions:

- **Sync now** — picks a direction by comparing when this service last saw the
  clipboard change against the age of the group's newest entry. It is disabled
  when the direction is not known: neither Windows nor macOS records when
  clipboard content arrived, so if the service has not observed a change since
  it started, it will not guess.
- **Upload this clipboard** — push local to the group.
- **Copy the latest entry here** — pull the group's newest entry onto this
  clipboard.

Text, images and files are supported. The relay accepts up to 50 MB of ciphertext
per entry. Anything over 10 MB travels in 8 MB chunks — the desktop and Android
apps do this on their own and show a progress bar while a large entry moves.
Against a relay that predates chunked transfer they fall back to its 10 MB cap
and say so before uploading.

**Watch the clipboard** (Settings) turns on polling: anything you copy is
encrypted and uploaded as the group's latest entry. It is off by default and
opt-in only. Content this service writes to your clipboard is never uploaded
back.

### History

The **History** tab lists entry metadata and fetches nothing until you ask.
Pick an entry to decrypt it and put it on the local clipboard. Entries expire
within 24 hours.

### Devices and revocation

The **Devices** tab lists the group's members with the current epoch. Revoking
a device generates a new group key and hands it to the remaining devices; the
revoked device loses access immediately and cannot rejoin without pairing
again. The confirmation dialog names the device and lists who keeps access.

Revocation never touches your local clipboard — the client core has no access
to a clipboard at all, which is what guarantees it structurally. Entries
written before the rotation stay readable only to whoever already had them, and
expire within a day.

### Settings

| Setting | Default | Notes |
|---|---|---|
| Watch the clipboard | off | Opt-in polling, as above. |
| Upload automatically up to | 25 MB | What watching the clipboard uploads on its own. Anything larger is left for **Upload**; a notice says so. Empty is no limit. |
| Download automatically up to | 25 MB | What taking other devices' uploads fetches on its own, decided before anything is downloaded. Anything larger is left for **Download** or History. Empty is no limit. |

When a limit holds something back, a notice at the top of the app says what and
how big — only the latest one, kept by the service, so it is there when you open
the app later. It stays until you dismiss it or a newer entry lands on either
side: something you copied is uploaded, or an entry is put on this clipboard.
| Start at login | off | A per-user login item: a launchd agent on macOS, the `HKCU\...\Run` key on Windows. |
| Localhost port | 47821 | Applies at the next launch. |
| This device's name | hostname | What your other devices show in their revocation dialog. Applies at the next launch. |

They are stored in `desktop.json` under your per-user config directory
(`%APPDATA%\TwoPlacePaste\` on Windows, `~/Library/Application
Support/TwoPlacePaste/` on macOS). The file is plain JSON on purpose: the port
has to be editable by hand after a bind failure. Nothing secret is in it.

**Leave the group** (Settings) disconnects this device and deletes the keys it
holds — the device private key and the group key both — leaving the app at its
setup screen with a new identity. It is how a desktop is moved from one group
to another: a device holds exactly one group key at a time.

It is local, and only local. Nothing is removed from the relay: the group still
lists the device, and the group key it held is still the group's key. To stop a
device that has left from reading what the group writes next, revoke it from
another device, which re-keys the group. Your clipboard is not touched, and
neither are the settings above.

---

## 4. Development

### Prerequisites

| Tool | Version | Needed for |
|---|---|---|
| Go | 1.26 | Every Go module. `GOTOOLCHAIN=auto` (the Makefile's default) lets an older `go` fetch it. |
| Node.js | 26 (24 works) | The desktop UI and the TypeScript protobuf bindings. |
| golangci-lint | v2.6.2+, built with Go 1.26 | `make lint`. |
| Docker | any recent | Redis and the deployment stack. |

`golangci-lint` refuses to analyse a module whose `go` directive is newer than
the Go it was built with, so install it from source with the matching
toolchain (CI does the same and caches the result):

```sh
GOTOOLCHAIN=go1.26.0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.6.2
```

### Everyday targets

```sh
make build        # compile every Go module
make test         # go test -race, every module
make lint         # go vet + golangci-lint, every module
make fmt          # gofumpt, falling back to gofmt
make tidy         # go mod tidy + go work sync
make proto        # regenerate protobuf bindings (Go + TypeScript)
make proto-check  # fail if the committed generated output is stale
make run-server   # go run ./cmd/tpp from server/
make help         # everything else
```

`make run-server` reads `.env` from the `server/` directory and needs a Redis
it can reach (`TPP_REDIS_ADDR`, default `127.0.0.1:6379`).

`make proto` needs no `protoc`: `proto/generate.sh` builds pinned `buf`,
`protoc-gen-go` and `ts-proto` from `proto/tools/go.mod` and
`proto/package-lock.json`. Generated output is committed and must stay
byte-identical.

### Running the desktop service from a checkout

```sh
cd desktop/ui && npm ci && npm run build
cd .. && go run ./cmd/tppdesktop
```

For UI work, run Vite and point the service at it — the token and `Origin`
checks still apply:

```sh
cd desktop/ui && npm run dev                          # http://127.0.0.1:5173
TPP_DESKTOP_UI_DEV=http://127.0.0.1:5173 go run ./cmd/tppdesktop
```

Desktop environment overrides (for developers; everything a user changes lives
in the settings file and the UI):

| Variable | Meaning |
|---|---|
| `TPP_DESKTOP_CONFIG_DIR` | Where `desktop.json` lives. Defaults to the per-user config directory. |
| `TPP_DESKTOP_UI_DEV` | Base URL of a running Vite server; the UI is proxied from there. |
| `TPP_DESKTOP_LOG` | `debug`, `info`, `warn`, `error`. Defaults to `info`. |

### Repository layout

```
proto/                  protobuf schema (shared contract) + codegen
spec/                   crypto spec + cross-language test vectors
server/                 Go module: relay + admin UI
  cmd/tpp               the binary: serve, gc --verify
  internal/config       .env + environment, validated in one pass
  internal/store        Redis persistent zone (groups, devices, wrapped keys)
  internal/entries      Redis ephemeral zone + entry service
  internal/blob         disk blob backend, expiry-bucket sweeper, verify
  internal/ws           WebSocket transport and protocol handlers
  internal/admin        admin UI, login rate limiting, creation tokens
  internal/httpapi      route assembly
pkg/tppclient/          Go module: shared client core
  tppcrypto             the crypto profile, validated against spec/vectors
  keystore              where secrets live at rest, per OS
desktop/                Go module: tray service + embedded React UI
  cmd/tppdesktop        the binary: logging, keystore, wiring, shutdown
  internal/service      sync direction, revocation gate, settings
  internal/localui      localhost server: token, Origin check, JSON API, events
  internal/clipboard    per-OS clipboard, watcher, self-write suppression
  internal/tray         tray icon and status, menu, notifications, visible bind failure
  internal/autostart    launchd agent (macOS), HKCU Run key (Windows)
  ui/                   the React app; builds into internal/localui/dist
mobile/                 React Native app (in development)
deploy/                 docker-compose, .env.example, redis.conf, Dockerfile
docs/                   specification, roadmap, deployment, conventions
```

The desktop module's own notes are in [`desktop/README.md`](desktop/README.md).

### Contributing

Read [`docs/conventions.md`](docs/conventions.md) first, then
[`docs/ROADMAP.md`](docs/ROADMAP.md).

The roadmap is executed **one phase per pull request**. Each phase declares the
paths it owns, and a PR must not touch anything outside them. If a phase needs
a change to a shared contract — `proto/`, `spec/crypto.md`, `go.work`, the
`Makefile`, or a CI workflow — it does not make the change: it opens an issue
tagged `contract-change`, which is merged as its own small PR.

**Standing prohibition:** no pin, favourite, or extend-TTL feature. Entry
lifetimes are immutable and the blob garbage collector (SPEC §4.5) depends
entirely on that.

## License

Not yet chosen.
