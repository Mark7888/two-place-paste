# CLAUDE.md

TwoPlacePaste is a self-hosted, end-to-end encrypted shared clipboard. A Go
relay server stores and forwards ciphertext; desktop (Windows, macOS) and
Android clients do all the crypto. **The server never sees clipboard content,
content type or filenames** — that property shapes most rules below.

Default branch: `master`.

## Repository layout

| Path | What it is |
|---|---|
| `server/` | Go module: the relay (`cmd/tpp`, binary `tpp`) — WebSocket protocol (`internal/ws`), Redis registry (`internal/store`) and entries (`internal/entries`), blob storage on disk or S3 plus the sweeper (`internal/blob`), quotas, admin UI (`internal/admin`, assets in `web/admin`), pairing page (`web/pair`), config (`internal/config`). |
| `pkg/tppclient/` | Go module: the shared client core and the only public Go API — crypto (`tppcrypto`), keystore, WebSocket client and every flow (pairing, groups, entries, chunked transfer). Generated protobuf in `protogen/` (do not edit). |
| `desktop/` | Go module: the tray service `tppdesktop`, a shell around `pkg/tppclient` — clipboard, tray, autostart, auto-updater, and a `127.0.0.1` HTTP server (`internal/localui`) that serves the React UI in `desktop/ui/` (Vite + TS, embedded from `internal/localui/dist`). |
| `mobile/` | React Native Android app (TypeScript). `src/core` = crypto + protocol, **no React Native imports**; `src/platform` = per-OS files (`*.android.ts` / `*.ios.ts`); `src/app` = screens and session; `src/protocol/gen` = generated protobuf (do not edit). |
| `proto/` | Protobuf wire schema `tpp.v1` (shared contract). |
| `spec/` | `crypto.md` (crypto profile) and `vectors/` — JSON test vectors both Go and TS clients must pass. |
| `deploy/` | Dockerfile, `docker-compose.yml`, `redis.conf`, `.env.example`. |
| `scripts/` | CI shell scripts (version, manifest, beta publish, GHCR cleanup), each with a `*_test.sh`. |
| `docs/site/` | User/operator docs site (Starlight). |
| `docs/internal/` | Contributor docs: `SPECS.md`, `ROADMAP.md`, `conventions.md`, `plans/`. |
| `go.work` | Workspace for `server`, `pkg/tppclient`, `desktop`. |

`docs/internal/SPECS.md` is the source of truth for behaviour; code and comments
cite it as `SPEC §n`. Read `docs/internal/conventions.md` before changing code —
reviewers hold PRs to it.

## Build, test, lint

Go 1.26 (`GOTOOLCHAIN=auto`), Node 24+, golangci-lint v2 built with Go 1.26.

```sh
make build test lint      # every Go module (test runs with -race)
make fmt / make fmt-check # gofumpt / gofmt
make tidy                 # go mod tidy + go work sync
make proto                # regenerate Go + TS bindings (no protoc needed)
make proto-check          # fail if committed generated code is stale
make run-server           # needs Redis; reads server/.env
make help
```

Run Go commands per module (`cd server && go test -race ./...`), not from the root.

- Redis-backed tests use `TPP_TEST_REDIS_ADDR` (default `127.0.0.1:6379`) and
  skip with a reason when Redis is absent. S3 tests use `TPP_TEST_S3_*` (MinIO).
- Desktop UI: `cd desktop/ui && npm ci && npm run build` — build it **before**
  `go run ./cmd/tppdesktop`, because the binary embeds it. `npm run typecheck`
  for type checks. Dev loop: `npm run dev` plus
  `TPP_DESKTOP_UI_DEV=http://127.0.0.1:5173`.
- Mobile: `cd mobile && npm ci && npm test && npm run lint && npm run typecheck`.
  `npm run interop` pairs the TS client with the Go client against a real
  relay (needs `go` and `redis-server`).
- Scripts: `scripts/<name>_test.sh`.
- Docs site: see below.

CI (`.github/workflows/`) runs per area on path filters: `go.yml` (build, vet,
`-race` tests, golangci-lint), `node.yml` (lint/typecheck/test/build for each
npm project), `proto.yml` (`make proto-check`), `desktop.yml`, `android.yml`,
`docker.yml`, `docs.yml`, `scripts.yml`. Run the matching local checks before
pushing.

## Code conventions (summary of `docs/internal/conventions.md`)

**Go**
- Wrap errors with one lowercase clause naming the operation and `%w`
  (`fmt.Errorf("apply rekey for group %s: %w", id, err)`). Use `errors.Is/As`;
  sentinel errors live in the owning package; aggregate with `errors.Join`.
- Logging is `log/slog` only, structured key/values, `…Context` variants in
  request paths. Only `main` configures logging.
- `ctx context.Context` is the first parameter of anything doing I/O; never in
  a struct; only `main` and tests create a background context.
- No panics in request paths. Every goroutine has an owner and a stop path;
  queues are bounded.
- All server time is UTC; time-dependent code takes a clock or `now`.
  Config durations are `time.Duration` strings.
- `internal/` for everything except `pkg/tppclient`. Define interfaces at the
  consumer. Exported identifiers get doc comments; package docs cite the SPEC.
- Prefer the standard library (`net/http` routing, no router). Call out any new
  module dependency in the PR with a reason.
- Tests: table-driven, `t.Parallel()`, standard library only (no testify),
  `got = %v, want %v` messages, `t.TempDir`/`t.Cleanup`, never `time.Sleep`
  to wait for a condition.

**TypeScript**
- `strict: true`; no `any` without a justifying comment; wrap errors with
  `{ cause }`.
- Crypto is `@noble/*` and must pass `spec/vectors`.
- Platform access only through `mobile/src/platform/`. `src/core` runs on
  Hermes: don't rely on `URL`/`URLSearchParams` setters, `Buffer`,
  `TextEncoder` or `atob` — use the hand-written helpers (`src/core/bytes.ts`,
  `src/core/protocol/urls.ts`).
- npm dependencies are pinned with `~<version>` (never `^`), and lockfiles are
  committed.

**Comments and docs** are written in plain prose that explains *why*; match
the existing tone. Line endings are LF everywhere (`.gitattributes`).

## Security rules (never weaken in a refactor)

- Never put clipboard content, content type, filenames, keys, wrapped keys,
  ciphertext, tokens, passwords or session cookies in a log or error message.
  IDs, sizes, epochs and timestamps are fine.
- Nothing in the protobuf schema may carry a content type, filename or
  plaintext length — those live inside the ciphertext.
- The desktop local server binds `127.0.0.1` only, requires the per-launch
  token on every request, and refuses a foreign `Origin` (including WebSocket
  upgrades) without demanding one.
- Admin cookies are `HttpOnly`, `Secure`, `SameSite=Strict`. Compare secrets
  with `crypto/subtle.ConstantTimeCompare`; randomness from `crypto/rand`.
- Enforce size limits at the reader (`io.LimitReader`) before buffering.
- Entry lifetimes are fixed at write time (24 h); no pin / extend-TTL
  features — the blob GC depends on it.

## Shared contracts and generated code

- `proto/**`, `spec/**`, `go.work`, the root `Makefile`, `.golangci.yml`,
  CI workflows and the licensing files (next section) are shared contracts: change them in a small, dedicated PR,
  not mixed into feature work.
- After editing `proto/`, run `make proto` and commit the regenerated
  `pkg/tppclient/protogen` and `mobile/src/protocol/gen`. Never hand-edit
  generated files. Enum numbers in the schema are permanent.
- A wire change must land in the server and every client together (see
  `docs/internal/plans/` for how past cross-cutting changes were done).
- No binary blobs beyond the existing icons/screenshots (e.g. the Gradle
  wrapper JAR is deliberately not committed).

## Licensing

Copyright (C) 2026 Mark Licsauer. The repository is split between two licenses;
`LICENSING.md` is the map and must stay in sync with this section.

| Path | License |
|---|---|
| Everything by default (root `LICENSE`) | MPL-2.0 |
| `server/` (`server/LICENSE`) | AGPL-3.0-or-later |
| `pkg/tppclient/` (`pkg/tppclient/LICENSE`, a copy of the root text so pkg.go.dev detects it) | MPL-2.0 |
| The name, logo and app icon: the artwork in `assets/icon/` and every icon generated from it | Not licensed |

- The server is AGPL to protect the hosted service; everything else is MPL so
  the apps can ship in app stores (Apple's terms conflict with the GPL family).
- New files take the license of their folder. Never move or copy code between
  `server/` and the MPL parts — that relicenses it, which only the copyright
  holder can do and never for outside contributions. Ask first.
- The `LICENSE` files are the official texts, unmodified. Explanations go in
  `LICENSING.md`, never into a `LICENSE` file (it would break GitHub's license
  detection and turn it into a custom license).
- Every `package.json` carries `"license": "MPL-2.0"` (npm mirrors it into the
  lockfile); a new npm project does the same.
- A new dependency that ships (in a binary, an app or the client core) must
  have a license usable under both MPL-2.0 and AGPL-3.0: MIT, BSD, ISC,
  Apache-2.0, MPL-2.0 and the like. No GPL, LGPL or AGPL dependencies outside
  `server/`, or the apps can no longer go to app stores. Build-only tools
  (such as the docs site's image pipeline) don't ship and don't count.
- Contributions are accepted under the license of the folder they change; there
  is no CLA (`CONTRIBUTING.md`). Copyright notices live in `LICENSING.md` and
  `README.md`, not in every source file.
- The AGPL asks a modified relay to offer its source to its users. Every page
  the relay serves to people (admin, pairing) links to `/source`, which
  redirects to `TPP_SOURCE_URL` (`internal/sourcelink`). A new human-facing
  page keeps that link; don't remove it.

## Versioning and releases

Versions come from git tags; nothing is written back to the repo
(`scripts/version.sh`, `docs/internal/plans/versioning-releases-and-updates.md`).
Pushing `vX.Y.Z` (or `vX.Y.Z-rc.N`) runs `release.yml`, which builds the
desktop, Android and Docker artefacts and publishes a GitHub Release. Builds on
`master` publish the rolling Beta channel; other branches are Nightly. Desktop
update manifests are signed — don't change `internal/update` trust checks
casually.

## Commits and PRs

- Commit subjects are short, imperative, sentence case, no prefix
  (`Fix CodeQL code-scanning alerts`). The body explains what changed and why,
  wrapped at ~72 columns, with bullets per area when a commit spans several.
- Keep PRs focused; update `docs/internal/ROADMAP.md` status when a roadmap
  phase lands.
- Behaviour or UI changes update the docs site in the same PR (next section).

## Keep the documentation site up to date

The user and operator documentation lives in `docs/site/` (Starlight, published
to https://mark7888.github.io/two-place-paste/). **Every change that adds a
feature or changes behaviour must update it in the same PR.** That includes:

- new or renamed settings, buttons, tabs, tray items or other UI text;
- install, update or uninstall steps on any platform;
- server configuration variables, defaults, endpoints, CLI commands or the
  Docker / compose setup;
- anything a user or operator would notice.

Pages are in `docs/site/src/content/docs/`; the sidebar is in
`docs/site/astro.config.mjs`. When the UI changes visibly, retake the affected
screenshots in `docs/site/src/assets/screenshots/`. Check the site still builds
(this also validates every internal link):

```sh
cd docs/site && npm ci && npm run build
```

Server config variables (`TPP_*`, parsed in `server/internal/config`) are
documented in `self-hosting/configuration.md` and `deploy/.env.example`; keep
all three in sync. Desktop environment variables are documented in
`desktop/README.md` and `contributing/development.md`.

Design documents for contributors (spec, roadmap, conventions, feature plans)
are separate and live in `docs/internal/`.
