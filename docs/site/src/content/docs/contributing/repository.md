---
title: Repository and conventions
description: How the repository is laid out and how changes are made.
---

## Layout

```
proto/                  protobuf schema (the shared wire contract) + codegen
spec/                   crypto spec + cross-language test vectors
server/                 Go module: relay + admin UI
  cmd/tpp               the binary: serve, gc --verify, gc --rebuild-accounting
  internal/config       .env + environment, validated in one pass
  internal/store        Redis persistent zone (groups, devices, wrapped keys)
  internal/entries      Redis ephemeral zone + entry service
  internal/blob         blob backends (disk, S3), expiry-bucket sweeper, verify
  internal/ws           WebSocket transport and protocol handlers
  internal/admin        admin UI, login rate limiting, creation tokens
  internal/httpapi      route assembly
pkg/tppclient/          Go module: shared client core
  tppcrypto             the crypto profile, validated against spec/vectors
  keystore              where secrets live at rest, per OS
desktop/                Go module: tray service + embedded React UI
  cmd/tppdesktop        the binary
  internal/service      sync direction, revocation gate, settings
  internal/localui      localhost server: token, Origin check, JSON API, events
  internal/clipboard    per-OS clipboard, watcher, self-write suppression
  internal/tray         tray icon and status, menu, notifications
  internal/autostart    launchd agent (macOS), HKCU Run key (Windows)
  internal/update       the auto-updater
  ui/                   the React app; builds into internal/localui/dist
mobile/                 React Native app (in development)
deploy/                 docker-compose, .env.example, redis.conf, Dockerfile
docs/site/              this documentation site
docs/internal/          specification, roadmap, conventions and design plans
```

## Design documents

These are written for contributors, not users, and live in the repository rather than on this
site:

- [`docs/internal/SPECS.md`](https://github.com/Mark7888/two-place-paste/blob/master/docs/internal/SPECS.md) — the specification.
- [`spec/crypto.md`](https://github.com/Mark7888/two-place-paste/blob/master/spec/crypto.md) — the crypto profile.
- [`docs/internal/conventions.md`](https://github.com/Mark7888/two-place-paste/blob/master/docs/internal/conventions.md) — engineering conventions. **Read this first.**
- [`docs/internal/ROADMAP.md`](https://github.com/Mark7888/two-place-paste/blob/master/docs/internal/ROADMAP.md) — the implementation roadmap.
- [`docs/internal/plans/`](https://github.com/Mark7888/two-place-paste/tree/master/docs/internal/plans) — design plans for individual features.

## Contributing

The roadmap is carried out **one phase per pull request**. Each phase declares the paths it owns,
and a PR does not touch anything outside them. A change to a shared contract — `proto/`,
`spec/crypto.md`, `go.work`, the `Makefile`, a CI workflow or a license file — goes in its own small PR, opened
from an issue tagged `contract-change`.

**Standing rule:** no pin, favourite or extend-TTL feature. Entry lifetimes are immutable, and the
blob garbage collector depends on it.

## Licensing

The repository is licensed under the Mozilla Public License 2.0, except `server/`, which is
licensed under the GNU Affero General Public License v3.0 or later. The TwoPlacePaste name and
logo are not covered by either license.
[`LICENSING.md`](https://github.com/Mark7888/two-place-paste/blob/master/LICENSING.md) has the
full map.

A contribution is licensed under the license of the folder it changes; there is no contributor
license agreement. Don't move or copy code between `server/` and the rest of the repository — that
would change its license. See
[`CONTRIBUTING.md`](https://github.com/Mark7888/two-place-paste/blob/master/CONTRIBUTING.md).

Every shipped build carries the licenses of the libraries it is built from, generated at build time
by `scripts/notices/notices.mjs`. The Docker build, the desktop UI's `npm run build` and the
Android build run it on their own; nothing about it is maintained by hand.
