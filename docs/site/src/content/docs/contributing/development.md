---
title: Development setup
description: Build, test and run TwoPlacePaste from a checkout.
---

## Prerequisites

| Tool | Version | Needed for |
|---|---|---|
| Go | 1.26 | Every Go module. `GOTOOLCHAIN=auto` (the Makefile's default) lets an older `go` fetch it. |
| Node.js | 26 (24 works) | The desktop UI, this documentation site, and the TypeScript protobuf bindings. |
| golangci-lint | v2.6.2+, built with Go 1.26 | `make lint`. |
| Docker | any recent | Redis and the deployment stack. |

`golangci-lint` refuses to analyse a module whose `go` directive is newer than the Go it was built
with, so install it with the matching toolchain:

```sh
GOTOOLCHAIN=go1.26.0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.6.2
```

## Everyday targets

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

`make run-server` reads `.env` from `server/` and needs a Redis it can reach (`TPP_REDIS_ADDR`,
default `127.0.0.1:6379`).

`make proto` needs no `protoc`: `proto/generate.sh` builds pinned tools from `proto/tools/go.mod`
and `proto/package-lock.json`. Generated output is committed and must stay byte-identical.

## Run the desktop app from a checkout

The UI must be built **before** the Go binary, because the binary embeds it:

```sh
cd desktop/ui && npm ci && npm run build   # builds into ../internal/localui/dist
cd .. && go run ./cmd/tppdesktop
```

For UI work, run Vite and point the service at it. The token and `Origin` checks still apply:

```sh
cd desktop/ui && npm run dev                          # http://127.0.0.1:5173
TPP_DESKTOP_UI_DEV=http://127.0.0.1:5173 go run ./cmd/tppdesktop
```

| Variable | Meaning |
|---|---|
| `TPP_DESKTOP_CONFIG_DIR` | Where `desktop.json` lives. Defaults to the per-user config directory. Handy for running two instances side by side. |
| `TPP_DESKTOP_UI_DEV` | Base URL of a running Vite server; the UI is proxied from there. |
| `TPP_DESKTOP_LOG` | `debug`, `info`, `warn`, `error`. Defaults to `info`. |
| `TPP_DESKTOP_GITHUB_TOKEN` | A GitHub token for the Nightly update channel, used instead of the stored one. |
| `TPP_DESKTOP_UPDATE_ALLOW_LOCAL` | `1` lets a build from a checkout install updates, to test the updater. |

On Linux the desktop build runs without a clipboard or tray and prints its URL to the terminal.

## This documentation site

The site lives in `docs/site/` and is built with [Starlight](https://starlight.astro.build).

```sh
cd docs/site
npm ci
npm run dev       # http://localhost:4321/two-place-paste/
npm run build     # static output in docs/site/dist, with link checking
```

Pages are Markdown or MDX files in `docs/site/src/content/docs/`; the sidebar is defined in
`docs/site/astro.config.mjs`. Screenshots live in `docs/site/src/assets/screenshots/`. Every push to
the default branch that touches `docs/site/` publishes the site to GitHub Pages.
