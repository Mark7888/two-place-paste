# TwoPlacePaste

Share your clipboard across devices.

A self-hosted, end-to-end encrypted shared clipboard. Copy on one device, paste
on another. The server relays ciphertext and never sees clipboard content.

> **Status: Phase 0.** Scaffolding, tooling and CI only. Nothing works yet —
> there is no server binary, no client, and no protocol. See
> [`docs/ROADMAP.md`](docs/ROADMAP.md) for what lands when.

---

## What it is

| Component | Stack | Purpose |
|---|---|---|
| Server | Go, Redis | Relay, group/device registry, admin UI |
| Desktop service | Go | Background tray service, localhost web UI |
| Mobile app | React Native | Manual sync, quick-settings tile, history browser |

**MVP platforms:** Windows, macOS, Android. iOS and Linux desktop are deferred.

The server is untrusted with content. Each group has one symmetric key shared by
its devices and rotated on every revocation; the server only ever holds it
wrapped to each device's public key. Entries expire after 24 hours.

Full design: [`docs/SPECS.md`](docs/SPECS.md).

## Repository layout

```
proto/                  protobuf schema (shared contract)
spec/                   crypto spec + cross-language test vectors
server/                 Go module: relay + admin UI
pkg/tppclient/          Go module: shared client core (crypto, WS client, flows)
desktop/                Go module: tray service + embedded React UI
mobile/                 React Native app
deploy/                 docker-compose, .env.example, redis.conf
docs/                   specification, roadmap, conventions
```

Only `server/`, `pkg/tppclient/`, `desktop/` and `docs/` exist today. The layout
is fixed in Phase 0 and is the basis of the roadmap's conflict isolation: each
phase owns a disjoint set of paths.

## Prerequisites

| Tool | Version | Notes |
|---|---|---|
| Go | 1.26 | Declared in every `go.mod`. `GOTOOLCHAIN=auto` (the Makefile's default) lets an older `go` fetch it. |
| Node.js | 26 (24 works) | Not needed until Phase 5c / 6a. |
| golangci-lint | v2.6.2+ | Must be **built with Go 1.26** — see below. |
| Docker | any recent | For Redis and the deployment stack (Phase 3e). |

`golangci-lint` refuses to analyse a module whose `go` directive is newer than
the Go it was itself built with. Install it from source with the matching
toolchain:

```sh
GOTOOLCHAIN=go1.26.0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.6.2
```

CI does the same thing, and caches the result.

## Getting started

```sh
git clone https://github.com/Mark7888/two-place-paste
cd two-place-paste

make build    # compile every Go module
make test     # go test -race, every module
make lint     # go vet + golangci-lint, every module
make help     # everything else
```

`make run-server` and `make proto` are wired up but report that their targets
land in Phases 3e and 1 respectively.

## Configuration

The server reads a `.env` file and the process environment; the process
environment wins. `deploy/.env.example` documents every key once Phase 3e lands.
Until then, `server/internal/config` is the source of truth — it validates
eagerly and reports every problem in one pass.

Exactly one of `ADMIN_PASSWORD` or `ADMIN_PASSWORD_HASH` must be set. MVP uses
the plaintext path; moving to a hash is a config change, not a code change.

## Contributing

Read [`docs/conventions.md`](docs/conventions.md) first, then
[`docs/ROADMAP.md`](docs/ROADMAP.md).

The roadmap is executed **one phase per pull request**. Each phase declares the
paths it owns, and a PR must not touch anything outside them. If a phase needs a
change to a shared contract — `proto/`, `spec/crypto.md`, `go.work`, the
`Makefile`, or a CI workflow — it does not make the change: it opens an issue
tagged `contract-change`, which is merged as its own small PR.

**Standing prohibition:** no pin, favourite, or extend-TTL feature. Entry
lifetimes are immutable and the blob garbage collector (SPEC §4.5) depends
entirely on that.

## License

Not yet chosen.
