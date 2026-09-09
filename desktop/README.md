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

Everything a user changes — the port, clipboard auto-watch, autostart — is in the
settings file and in the UI, not in the environment.

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
