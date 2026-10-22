# Plan — Admin group management, tags, storage overrides and housekeeping

**Status:** implemented. Step 0 merged as #35; steps 1–5 are on one branch, since they
were asked for together. §8 records where the build differs from the plan below, which is
kept as it was agreed.
**Touches:** `/proto/**` (step 0), `/server/**` (all steps), `/pkg/tppclient/**`,
`/desktop/**` and `/mobile/**` (steps 4 and 5), `/docs/site/**`, `/deploy/.env.example`, SPEC §4.4.

---

## 1. Goals

Give the admin real control over groups without giving them any access to
content (SPEC §2.3, §4.4 stay true):

1. **Group detail page:** rename a group, see its devices (name, created, last
   seen, online now). The apps show the group's name too.
2. **Tags:** put groups under a tag ("family", "friends"), and filter and total
   the dashboard by tag.
3. **Delete a group** from the admin UI, with clients telling the user what
   happened.
4. **Purge a group's entries** to free its storage at once.
5. **Health checks:** Redis eviction policy, AOF, memory, server version and
   uptime.
6. **Sweep now** button.
7. **Token improvements:** optional expiry, copy-link button, a note, and a tag
   the resulting group inherits.
8. **Stale groups:** highlight groups inactive for N days, and bulk-delete them.
   N is set on a new **Settings** page of the admin UI.
9. **Audit log** of admin actions.
10. **Storage limit overrides** with the precedence `default < tag < group`.

Out of scope: removing a single device from the admin UI (the server cannot
rekey, SPEC §3.3), and any per-entry view (SPEC §4.4: sizes and counts only).

## 2. What exists today

| Piece | Where | State |
|---|---|---|
| Dashboard: per-group devices, last active, entries, oldest, blob usage | `server/internal/admin/dashboard.go`, `web/admin/dashboard.html` | Done |
| Group "name" | The creation token's name (`store.Token.Name`); the group ID is the token value | No separate group name |
| Tokens: create, QR, revoke unused | `server/internal/admin/handlers.go`, `web/admin/tokens.html` | Done |
| Per-group blob limit | `TPP_BLOB_MAX_GROUP_BYTES`, passed as `ARGV[9]` to the reserve script in `server/internal/quota/redis.go` | One global value |
| Sweeper | `server/internal/blob/sweeper.go` (`Run`, `sweepOnce`, `Status`) | Interval only |
| Hub | `server/internal/ws/hub.go` (`broadcast`, `closeDevice`) | Knows who is online |
| Client reaction to an unknown device | WS upgrade answers 401 → `tppclient.ErrRevoked` → desktop `OnRevoked`, mobile `probablyRevoked` | Says "this device was revoked" |

## 3. Decisions

| # | Decision | Why |
|---|---|---|
| D1 | The group name is a `name` field on `group:<id>`. When it is empty the token name is shown, so existing groups need no migration. Renaming does not touch the token. | Admin-only metadata the server already knows; no E2E impact. |
| D2 | A group has **at most one tag** (§7, answer 2). Tags are stored as `tag:<name>` hashes (storage override) plus a `tags` set; the group's tag is a `tag` field on `group:<id>`. Deleting a tag untags its groups. | One tag keeps the override precedence unambiguous: a group in two tags would have two candidate limits. |
| D3 | Storage overrides are `{bytes, enabled}` pairs on the tag and on the group. The effective per-group limit is the first **enabled** override of group, then tag, then `TPP_BLOB_MAX_GROUP_BYTES`. Unticking keeps the stored bytes. | As requested: the env value is the default, the checkbox decides whether the override counts. |
| D4 | The effective limit is resolved **inside the reserve Lua script** from `group:<id>` and `tag:<name>`, not passed from Go. | No race between a rename/retag and an upload; one round trip. |
| D5 | An override may not exceed `TPP_BLOB_MAX_TOTAL_BYTES` (when that is set); `0` means "no per-group limit", as it does for the env value. Lowering a limit below current usage evicts **nothing at once**. The next upload to that group evicts oldest-first until usage plus the new upload fits the new limit, which is what the reserve script already does with any limit. The dashboard flags the group as over its limit until then. | Matches the existing eviction path (plan s3-blob-storage D5); no surprise mass deletion from a form submit. |
| D6 | Group deletion is one Lua script: group hash, device set, device hashes, wrapped keys, entry index and hashes, inline blobs, quota usage and index, pending pairings/offers. Blob files are deleted after the script, best effort; anything missed is reclaimed by the normal sweep (expiry paths, SPEC §4.5). The token record stays, marked "group deleted". | Atomic for everything that decides access; blobs are already safe to leave to the sweeper. |
| D7 | Deletion leaves a **tombstone** `deleted_device:<id>` per former device, with a TTL (§7, answer 6). The WS upgrade answers **410 Gone** for a tombstoned device, 401 otherwise. Live sockets are closed with WebSocket close code **4410** ("group deleted"). No protobuf change. | Lets an offline device learn *why* it lost access when it comes back. TTL keys are correct under `volatile-lru` (losing one only degrades the message to "revoked"). |
| D8 | `tppclient` maps 410 and close code 4410 to a new `ErrGroupDeleted` (wrapping `ErrRevoked`, so existing callers keep working). Desktop and mobile show "This group was deleted by the server admin" and offer to create or join a group. | Same exit path as revocation, clearer message. |
| D9 | Token expiry is optional, chosen at creation (never / 1 day / 7 days / 30 days). An expired token behaves like an unknown one (404, cannot create). The token list shows it as expired and lets the admin delete it. | Leaked unused links stop working by themselves. |
| D10 | Stale threshold is a server setting edited on a new admin **Settings** page (`/admin/settings`), stored in a Redis hash `settings` (persistent zone, no TTL), default 90 days. No env variable. | The admin asked for it in the UI; the page is the home for later runtime settings. |
| D11 | The audit log is a capped Redis list `audit` (last 1000 entries, no TTL): time, action, target ID or name, result, and for logins the client IP. Never tokens, passwords, cookies or keys. | Fits the logging rules in CLAUDE.md; small and bounded. |
| D12 | Health checks read `CONFIG GET maxmemory-policy`, `appendonly`, and `INFO memory` once per dashboard load. A failing `CONFIG` (managed Redis that disables it) shows "unknown", not an error. | SPEC §4.2: `allkeys-lru` destroys pairings silently; the admin should see it. |
| D13 | "Sweep now" signals the sweeper's existing goroutine over a buffered channel of 1; a sweep already queued or running absorbs the click. | Keeps one owner for the sweep loop (conventions: every goroutine has an owner). |
| D14 | Every destructive action (delete, bulk delete, purge) goes through a confirmation page that names the group(s), device counts and storage to be freed. | Irreversible; the admin UI is publicly reachable. |
| D15 | The group name reaches the apps through new messages, not existing ones: `GroupInfoRequest`/`GroupInfoResponse` (the client asks on every connect) and a `GroupRenamed` event. All protobuf changes for this plan land in **one dedicated PR** (step 0). Deletion needs none (D7). | Additive messages; old clients ignore the event and never send the request. Proto is a shared contract (CLAUDE.md). |

## 4. Work items

### 4.1 Store — `server/internal/store`

- `Group` gains `Name`, `Tag`, `Override{Bytes int64; Enabled bool}`.
- New: `RenameGroup`, `SetGroupTag`, `SetGroupOverride`, `ListTags`,
  `PutTag` (name + override), `DeleteTag`, `DeleteGroup` (D6, returns the blob
  refs to delete and the device IDs for the hub), `IsTombstoned(deviceID)`.
- `Token` gains `Note`, `Tag`, `ExpiresAt`. `ConsumeToken`/`CreateGroup`
  refuse an expired token and copy its tag onto the new group.
- Memory store mirrors all of it; tests table-driven against both, Redis tests
  skip without `TPP_TEST_REDIS_ADDR`.

### 4.2 Quota — `server/internal/quota`

- Reserve script resolves the limit per D3/D4; `ARGV[9]` becomes the default.
- `Purge(ctx, groupID)` releases every entry of a group (used by purge and
  delete).
- Tests: precedence table (default only, tag on/off, group on/off, both, 0 =
  unlimited), and lowering below usage evicts only on the next reserve.

### 4.3 Entries and blobs

- `entries.PurgeGroup(ctx, groupID)`: deletes the group's entries and inline
  blobs, pushes `EntryEvicted` for each so open History lists update, then
  deletes the blobs.
- `blob.Sweeper.Trigger()` (D13).

### 4.4 WebSocket — `server/internal/ws`

- `Hub.Online(groupID) map[string]bool` for the device list.
- `Hub.closeGroup(groupID, code 4410)`.
- Upgrade path: 410 for tombstoned devices (D7).

### 4.5 Admin UI — `server/internal/admin`, `server/web/admin`

New and changed routes (all behind the session, POSTs with the existing
SameSite=Strict protection):

| Route | Purpose |
|---|---|
| `GET /admin/` | Dashboard: tag filter and group-by, per-tag totals, stale and over-limit badges, health card, "Sweep now" |
| `GET /admin/groups/{id}` | Detail: name, tag, override, devices with online dot, entry and storage stats |
| `POST /admin/groups/{id}/rename`, `/tag`, `/override` | Edits |
| `GET`/`POST /admin/groups/{id}/purge`, `/delete` | Confirm page + action (D14) |
| `GET`/`POST /admin/groups/delete` | Bulk delete of selected (stale) groups |
| `GET /admin/tags`, `POST /admin/tags`, `POST /admin/tags/{name}`, `POST /admin/tags/{name}/delete` | Tag list with group counts and the override checkbox |
| `POST /admin/sweep` | Sweep now |
| `GET /admin/audit` | Audit log, newest first |
| `GET`/`POST /admin/settings` | Server settings: stale threshold |
| `POST /admin/tokens` | Gains note, tag, expiry; list gets a copy-link button |

Every page keeps the `/source` link (AGPL, CLAUDE.md). Copy-link is the only
new JavaScript; it degrades to a selectable field.

### 4.6 Clients (steps 4 and 5)

- `pkg/tppclient`: `ErrGroupDeleted` (D8), tests for 410 and close 4410.
- Desktop: `OnRevoked` message branches on the error; UI shows the
  "deleted by admin" text and the create/join actions.
- Mobile: `src/core/protocol` maps the same two signals; `SyncScreen` shows the
  message. Interop test (`npm run interop`) covers a deletion.
- Group name (step 5): `tppclient` sends `GroupInfoRequest` after connecting
  and handles `GroupRenamed`; desktop shows the name in the tray tooltip and
  the UI header, mobile on the main screen. Empty name → no label.
- The server side of step 5 answers `GroupInfoRequest` and broadcasts
  `GroupRenamed` from the rename handler.

### 4.7 Config and docs

- No new env variable; the stale threshold lives on the Settings page.
- `docs/site/.../self-hosting/admin.mdx`: every new screen and button, the
  override precedence, what deletion does to devices; retake admin screenshots.
- User docs: what a device shows when its group is deleted.
- SPEC §4.4 rewritten for the new scope; SPEC §3 gains "Deletion by the admin".
  The s3-blob-storage plan's D10 ("a group is removed by revoking its devices")
  is superseded; note it there.

## 5. Order of work (one PR each)

0. **Protobuf (own branch and PR):** `GroupInfoRequest`, `GroupInfoResponse`,
   `GroupRenamed`, regenerated Go and TS bindings. Nothing uses them yet.
1. **Read-only and metadata (server only):** group detail page with devices and
   online state, rename, tags, health card, sweep now, token note/tag/expiry/copy,
   stale badges, audit log. No client change.
2. **Storage overrides (server only):** D3–D5, tag and group forms, over-limit
   badge.
3. **Purge entries (server only):** uses the existing `EntryEvicted` event, so
   clients already handle it.
4. **Group deletion (server + `tppclient` + desktop + mobile):** D6–D8, bulk
   stale delete, SPEC update. Wire change lands everywhere at once.
5. **Group name in the apps (server + `tppclient` + desktop + mobile):** uses
   the messages from step 0.

Each PR updates the docs site and `docs/internal/ROADMAP.md`.

## 6. Testing

- Go: table-driven, `t.Parallel()`, both store implementations, Lua scripts
  against real Redis when available.
- Admin handlers: every new POST refuses without a session, and the
  confirmation pages render the right counts.
- A test asserting no new admin page or audit entry contains a token value,
  wrapped key or entry ID beyond what SPEC §4.4 allows.
- Client: 410 and 4410 map to `ErrGroupDeleted` in Go and TS; desktop and
  mobile UI tests for the new message.

## 7. Answered questions

1. Override levels are env default < tag < individual group. **Yes.**
2. One tag per group. **Yes.**
3. Overrides change the per-group blob storage limit only, not the largest
   single upload. **Yes.**
4. Lowering a limit below usage: the next upload evicts as many entries as it
   takes to fit the new limit (D5).
5. The apps show the admin's group name; every protobuf change of this plan
   goes in one separate branch and PR (D15, step 0).
6. Tombstones for a deleted group's devices live 30 days (D7).
7. Token expiry: never (default), 1, 7 or 30 days (D9).
8. Stale threshold: 90 days by default, set on the admin Settings page (D10).

## 8. Where the build differs

1. **D4, where the limit is resolved.** The admin server resolves default < tag <
   group from the store and writes the result to the ledger's own `quota:limits`
   hash; the reserve script reads that hash, so a change still lands wholly before or
   after an upload. This keeps `internal/quota` inside its own key namespace instead of
   reading `group:` and `tag:` hashes. The server re-applies every group's limit at
   startup (`admin.Server.SyncStorageLimits`), and `tpp gc --rebuild-accounting` keeps
   the hash.
2. **D8, the Go handler.** `tppclient` has an `OnGroupDeleted` handler, which fires
   instead of `OnRevoked` when set; `ErrGroupDeleted` wraps `ErrRevoked` as planned.
3. **D7 on Android.** React Native's WebSocket does not expose the status of a refused
   upgrade, so the TS client asks the same `/ws?device_id=` URL with a plain GET when a
   dial is refused. The relay checks the device before it upgrades, so the GET gets the
   same 410 or 401. A live socket is told by the 4410 close code, as planned.
4. **Interop.** The deletion path is covered by a Go integration test against the real
   relay (`pkg/tppclient/admin_integration_test.go`) and by the TS client's fake-relay
   tests, not by `npm run interop`.
5. **Build version on the health card** comes from the Go build info. The Docker build
   has no VCS data in its context, so the image shows "development build" until the
   version is passed in with `-ldflags`, which needs a change to `deploy/Dockerfile` and
   `docker.yml` (a CI workflow, so its own PR).
6. **No bulk delete.** Goal 8's bulk deletion of stale groups, and the
   `/admin/groups/delete` routes, were built and then taken out at review: a
   deletion cuts a set of devices off for good, so each one is made from the
   group's own page. Stale groups are still marked on the dashboard.
