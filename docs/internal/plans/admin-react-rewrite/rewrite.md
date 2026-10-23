# Plan — The admin UI as a React app

**Status:** done.
**Touches:** `server/ui` (new), `server/web/admin` (now only the embedded build),
`server/internal/admin`, `deploy/Dockerfile`, CI (`node.yml`, `go.yml`, `docker.yml`).

---

## 1. Purpose

The admin UI started as one server-rendered screen (ROADMAP P4) and grew to six screens,
a page per group and two confirmation pages, all Go templates plus a hand-written script.
This rewrites it as a React app embedded in the relay the way the desktop app embeds its
UI, so that later work — the SmartTable (`smart-table.md`) first — has components, hooks
and a typed API to build on.

The rewrite changes how the screens are made, not what they are: the same screens, text,
markup and stylesheet, and the same behaviour (§5 lists the few differences).

## 2. Layout

| Path | What it is |
|---|---|
| `server/ui/` | Vite + React + TypeScript. Licensed AGPL-3.0-or-later like the rest of `server/`. |
| `server/ui/src/api/` | `client.ts` (fetch, CSRF header, errors), `types.ts` (the JSON), `index.ts` (one function per endpoint). |
| `server/ui/src/router.tsx` | A small History API router; also carries the one-off notice ("Group renamed."). |
| `server/ui/src/session.tsx` | Signed in or not, and the CSRF token. |
| `server/ui/src/hooks/` | `useResource` (load, reload, cancel), `useAction` (a change and its error), `useCopyFeedback`, `useDocumentTitle`. |
| `server/ui/src/components/` | The shell (`Layout`), notices, cards, tables, badges, meters, the token dialog. |
| `server/ui/src/pages/` | One component per screen. |
| `server/ui/src/lib/` | Formatting (sizes, times — ported from the Go code it replaces, with tests), clipboard, paths. |
| `server/web/admin/dist/` | The build, written by `npm run build`, embedded by `embed.go` with `all:dist`. Only `.gitkeep` is committed. |
| `server/internal/admin/` | The JSON API, the page and asset routes (`ui.go`), and everything it decided before. |

## 3. Serving

- Each screen's path (`/admin/`, `/admin/groups/{id}/purge`, …) serves `index.html`;
  `register.go` lists them, and any other path under `/admin/` stays a 404. Without a
  session every screen but `/admin/login` redirects there before the app loads, and
  `/admin/login` with one redirects to `/admin/`, as before.
- The bundle is `/admin/assets/app.js` and `/admin/assets/index.css`, plus the favicons.
  Fixed names, `Cache-Control: no-cache`.
- The page keeps its Content-Security-Policy and adds only `connect-src 'self'` for the
  API: `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self';
  connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'`.
- A binary built without the UI serves a placeholder that says how to build it, and keeps
  the `/source` link. Tests inject a stand-in build through `Options.Assets`.

## 4. The API

Everything is under `/admin/api/`, JSON in and out. Errors are `{"error": "…"}` with a
message written for the screen. Sizes are bytes and times RFC 3339 UTC; the app formats
them.

- **Session.** The session is still the `HttpOnly`, `SameSite=Strict` cookie on `/admin`.
  `POST /login` (rate limited as before) sets it and returns `{csrf}`; `GET /session`
  returns `{csrf}` or 401; `POST /logout` ends it.
- **CSRF.** Every request that changes something needs the session's token in
  `X-CSRF-Token`, compared in constant time; without it, 403. A cross-site page cannot
  set that header without a preflight the relay never answers.
- **401** from any endpoint means the session is gone; the app goes to the sign-in screen.

| Method and path | Does |
|---|---|
| `GET /dashboard` | Counts, storage, health, problems. |
| `POST /sweep` | Queues a sweep: `{queued}`. |
| `GET /groups[?tag=name\|-][&stale=1]` | One page of the groups, with the tag filter row. |
| `GET /groups/{handle}` | One group: its row, devices, limits, tags. |
| `PUT /groups/{handle}/name`, `/tag`, `/limit` | Rename, tag, own storage limit (`{enabled, size}`). |
| `POST /groups/{handle}/purge` | `{entries, bytes}`. |
| `DELETE /groups/{handle}` | `{name}`, or 404 if it was already gone. |
| `GET /tags`, `POST /tags`, `PUT /tags/{name}/limit`, `DELETE /tags/{name}` | Tags. |
| `GET /tokens[?status=…]`, `POST /tokens`, `DELETE /tokens/{handle}` | Tokens. `POST` returns the new token with its URL, so the screen opens its code without the value ever being in a URL. |
| `GET /tokens/{handle}/link` | An unused token's creation URL, for Copy link and Show code. |
| `GET /settings`, `PUT /settings` | The stale threshold. |
| `GET /audit[?action=…]` | One page of the audit log. |

The four lists take `q`, `sort`, `dir`, `page` and `size` and answer with one page plus what
was applied (`smart-table.md`). Groups and tokens are named by a handle, never by their id
or value (`handle.go`). The QR image stays a session-checked
`GET /admin/tokens/{handle}/qr.png`, since it is an `<img>`.

## 5. What behaves differently

- The page needs JavaScript. The old token screen's `<noscript>` fallback is gone.
- A notice after a change is held by the app, not the session: it shows once, and a
  reload or the next navigation drops it, as before.
- A refused change keeps what was typed and shows the error at the top, where a refused
  form used to come back with the stored values.
- Relative times ("12 min ago") are computed from the browser's clock rather than the
  relay's.

## 6. Installable app and phones

Added after the rewrite. The admin is a PWA: `public/manifest.webmanifest`, icons generated by
`assets/icon/generate.py`, and a service worker (`sw/sw.js`, built to `/admin/sw.js` by a small
Vite plugin with the precache list and a content-derived version). The worker is network first
and keeps only the app's static files plus one copy of the page; it never handles `/admin/api/`
or QR images, so nothing private reaches a cache. The CSP adds `manifest-src 'self'` and
`worker-src 'self'`. On phones the header folds into a **Menu** button, list rows become compact
cards (`td.name` is the heading; `wide`, `usage` and `actions` cells span the card), controls are
at least 44px and fields 16px (iOS zooms below that).

An open admin, installed or in a tab, notices an upgrade. The same plugin gives each build an id,
a digest of the built files, so it changes with any change to the UI whether the build is a
release, a channel build or a local checkout, and does not change for a server-only upgrade. The
id is written into the bundle, into the worker's version and into `/admin/version.json`
(public, `no-cache`). The app checks that file on start, every five minutes while visible, and
when it returns to the foreground or comes back online (at most every 30 seconds); when the ids
differ it asks the worker to update and shows a bar at the bottom with **Update** (reload) and
**Later**. It never reloads on its own, which would lose a half-filled form.

## 7. Checks

- Go: the API, the page routes, the CSP and the placeholder are covered in
  `server/internal/admin/*_test.go`, independently of any UI build.
- UI: `npm test` (formatting, the SmartTable, the update bar), `npm run typecheck`, `npm run build`, in `node.yml`.
- During the rewrite every screen was screenshotted against the template version at
  desktop and phone widths and matched it pixel for pixel, but for data (uptime, paths)
  and sub-pixel text shaping.
