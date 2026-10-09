# TwoPlacePaste — desktop service

A background service with a tray icon for Windows and macOS (SPEC §7.2). It is a
shell around [`pkg/tppclient`](../pkg/tppclient): every key, every frame and every
flow of SPEC §3 and §6 lives there. What lives here is the OS clipboard, the tray,
the login item, and a localhost HTTP server that serves the React UI to the user's
browser.

## Running it from a checkout

```bash
cd ui && npm ci && npm run build   # builds into internal/localui/dist
cd .. && go run ./cmd/tppdesktop
```

The tray opens `http://127.0.0.1:47821/app?token=<per-launch token>` in the default
browser. Without the UI build the service still runs; it serves a page saying so.

For UI work, run Vite and point the service at it:

```bash
cd ui && npm run dev                                  # http://127.0.0.1:5173
TPP_DESKTOP_UI_DEV=http://127.0.0.1:5173 go run ./cmd/tppdesktop
```

The token and Origin checks apply in dev mode too.

### Environment

| Variable | Meaning |
|---|---|
| `TPP_DESKTOP_CONFIG_DIR` | Where `desktop.json` lives. Defaults to a per-user config directory. |
| `TPP_DESKTOP_UI_DEV` | Base URL of a running Vite server; the UI is proxied from there. |
| `TPP_DESKTOP_LOG` | `debug`, `info`, `warn`, `error`. Defaults to `info`. |
| `TPP_DESKTOP_GITHUB_TOKEN` | A GitHub token for the Nightly update channel, used instead of the stored one. Never written anywhere. |
| `TPP_DESKTOP_UPDATE_ALLOW_LOCAL` | `1` lets a build from a checkout install updates, to test the updater. Otherwise it never replaces itself. |

Everything a user changes — the port, clipboard auto-watch, applying other
devices' entries, autostart — is in the settings file and in the UI, not in the
environment.

## Layout

| Path | What it holds |
|---|---|
| `cmd/tppdesktop` | The binary: logging, keystore, wiring, shutdown. |
| `internal/service` | Sync direction (SPEC §6), the revocation gate (SPEC §3.3), settings. |
| `internal/localui` | The localhost server: token, Origin check, embedded UI, JSON API, event stream. |
| `internal/clipboard` | Per-OS clipboard plus the watcher and its self-write suppression. |
| `internal/tray` | Tray icon and menu, and the visible bind failure. |
| `internal/autostart` | launchd agent (macOS) and the HKCU Run key (Windows). |
| `internal/config` | The settings file. Non-secret by design: the port must be editable by hand. |
| `internal/update` | The auto-updater: Stable, Beta and Nightly sources, manifest signatures, the in-place swap. |
| `internal/buildinfo` | The version CI stamps into the binary. |
| `ui/` | The React app. Builds into `internal/localui/dist`, which the binary embeds. |

## Security notes

Anything in the user's browser can reach `127.0.0.1`, so:

- the listener is `127.0.0.1` explicitly, never `0.0.0.0`;
- a token generated per launch is required on **every** request, and only the tray
  knows it. The UI captures it from the URL once, keeps it in memory, and clears the
  address bar — never `localStorage`, never a cookie;
- the `Origin` header is checked on every request, the WebSocket upgrade included;
- a bind failure is shown in the tray with the settings file that overrides the port.
  The service never fails silently.

Secrets are the keystore's, not this module's: the device private key and the group
key never reach the settings file or a log.

## Windows has no console, so there is a log file

The Windows build is linked with `-H=windowsgui`. Without it the binary is a
console application and Windows opens a black console window behind the tray
icon for as long as the service runs.

A GUI-subsystem process has no usable stderr, so the service writes
`tppdesktop.log` beside its settings file and mirrors it to stderr where stderr
works. The file is truncated at start-up once it passes 2 MiB: it exists to
explain the launch that just failed, not to be an archive. It carries the same
lines stderr would, which means no clipboard content and no key material.

On macOS the shipped artefact is `TwoPlacePaste.app`, whose `Info.plist` sets
`LSUIElement`, so there is no Dock icon and no window. Running the bare
`tppdesktop` binary from a shell will of course keep that shell — that is the
shell you started it from, not something the service opened.

## The browser opens once, not every launch

Startup opens the UI **only when this device is not in a group yet**, because
that is the one state where nothing works until the user does something and the
UI is the only place to do it. A paired device starts quietly into the tray: it
is a background service, and one that throws a browser tab at you on every
login is one you turn off. The tray's "Open UI" is how you reach it after that.

## What it costs to leave running

The service is meant to sit in the tray all day, including on a laptop's
battery, so it does nothing on a timer that it can avoid:

- **Auto-watch off** (the default): no clipboard timer at all. The relay
  socket idles, with a ping every 25 seconds to keep proxies from cutting it.
- **Auto-watch on**: every 750ms the watcher reads the clipboard's change
  counter — `GetClipboardSequenceNumber` on Windows, `NSPasteboard`'s
  `changeCount` on macOS — and reads the content only when the counter has
  moved. On macOS the content read is a few helper processes (`osascript`,
  `pbpaste`), so this is the difference between one cheap call per poll and
  several processes per poll.
- **Screen locked**, or another user switched in (macOS): the watcher stops
  reading and checks every 3 seconds whether anyone is back.
- **Sleep**: nothing runs, and the service never holds the machine awake.
  On wake it notices within 5 seconds (the wall clock has jumped past the
  monotonic one), drops the relay socket the sleep killed, reconnects at once,
  and fetches anything another device wrote meanwhile — which auto-apply then
  handles like any other new entry.
- **Auto-update on**: on macOS, `NSBackgroundActivityScheduler` decides when
  to check, about every six hours, and holds the check back on battery, under
  thermal pressure and in Low Power Mode. Elsewhere a ticker wakes every 30
  minutes to compare two timestamps. Either way a check only goes out when the
  last one is six hours old, and an update only restarts the service when the
  screen is locked or nothing has synced for ten minutes. Off, there is no
  timer at all.

## Updates

The Updates section of Settings, and the tray's *Check for updates…*, follow
one of three channels (docs/plans/versioning-releases-and-updates.md):

| Channel | Installs | Needs |
|---|---|---|
| Stable | The latest GitHub Release. | Nothing. |
| Beta | The newest build of the default branch, from the `channel-beta` prerelease. | Nothing. |
| Nightly | The build of one commit you enter, from GitHub Actions. | A fine-grained token with Actions: Read-only on this repository, kept in the keystore. |

Every download is checked against a manifest signed in CI with a key whose
public half is `internal/update/update-signing.pub.pem`. A fork's pull request
build cannot be signed; Nightly installs one only after showing where it came
from and asking. The previous build is kept for one-click rollback. A build
from a checkout (version `0.0.0-local`) never replaces itself.

