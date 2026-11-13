# Plan — Public admin REST API with API keys

**Status:** agreed, not started. Everything lands on one branch and one PR, as asked
(§9, answer "one PR"); the shared contracts it touches (`Makefile`, CI workflows,
licensing files) go in their own commits inside that PR.
**Touches:** `/api/**` (new), `/server/**`, `/docs/site/**`, `/deploy/.env.example`,
`Makefile`, `.github/workflows/go.yml` and `docs.yml`, `LICENSING.md`, `CLAUDE.md`,
SPEC §4.4, ROADMAP.

---

## 1. Goals

Let operators automate their relay — mint creation tokens from a script, watch
storage from monitoring, clean up stale groups from a cron job — through a
documented, versioned API instead of the admin UI's private one.

1. A **REST API under `/api/v1/`**, separate from the UI's `/admin/api/`, that
   covers what the admin screens can do (§4.4).
2. **API keys** with scopes, expiry, revocation and last-use tracking, created
   and revoked from the admin UI or the `tpp` command line — never through the
   API itself.
3. **Rate limiting** per key, and per address for failed authentication.
4. An **OpenAPI 3.1 description** in `api/openapi.yaml` (MPL-2.0), from which the
   relay's Go server code is generated and which the relay serves.
5. A **generated API reference** on the docs site, reached from an **API docs**
   button, plus a hand-written guide page.

Out of scope: content access of any kind (SPEC §2.3 — there is none to give),
the WebSocket transport and the client-facing endpoints, browser access to the
API (no CORS), and managing API keys through the API.

## 2. What exists today

| Piece | Where | State |
|---|---|---|
| Admin JSON API | `server/internal/admin/register.go`, `/admin/api/*` | Private to the React app: session cookie + `X-CSRF-Token`, UI-shaped JSON (`tablePage`, labels, sentences as errors), no versioning |
| Naming of groups and tokens | `server/internal/admin/handle.go` | Opaque handles; a group id is its creation token, a credential, and is never exposed |
| Lists | `server/internal/admin/table.go` | `q`, `sort`, `dir`, `page`, `size` parsed server-side |
| Rate limiting | `server/internal/admin/ratelimit.go` | In-memory token buckets, per IP + global, login only |
| Audit log | `server/internal/admin/audit.go`, `store.AuditEntry` | Action, target, detail, IP; no notion of *who* beyond "an admin" |
| CLI | `server/cmd/tpp` | `serve`, `gc` |
| Docs site | `docs/site` | Starlight 0.42 / Astro 7, no API reference |

## 3. Decisions

| # | Decision | Why |
|---|---|---|
| D1 | The API lives at **`/api/v1/`**. The admin UI keeps `/admin/api/` and may keep changing it freely. | The UI's API is shaped for one screen at a time; making it a contract would freeze the UI. The session cookie is scoped to `/admin`, so it is never even sent to `/api/v1/` and the API needs no CSRF defence. |
| D2 | Authentication is **`Authorization: Bearer <key>`** only. No cookies, no query-string keys. | Keys in URLs end up in proxy logs and browser history. |
| D3 | Key format: **`tpp_ak_<id>_<secret>`**. `<id>` is 8 characters of lowercase base32 (40 bits, public, shown in lists, logs and the audit log). `<secret>` is 32 bytes of `crypto/rand` in lowercase base32 (52 characters). | The fixed prefix lets GitHub secret scanning and similar tools spot a leaked key; base32 keeps `_` a safe separator; the id lets the server find the key without scanning and lets logs name it without leaking it. |
| D4 | Redis stores **SHA-256 of the secret**, never the secret. Comparison is `crypto/subtle.ConstantTimeCompare`. The full key is shown **once**, at creation. | 256 bits of entropy needs no slow hash; a Redis dump does not hand out working keys. |
| D5 | Keys live in `apikey:<id>` hashes plus an `apikeys` set, **without a Redis TTL**. Expiry is an `expires_at` field checked on every request. | The deployment runs `volatile-lru` (SPEC §4.2): a key with a TTL is evictable, and an evicted API key would silently stop working. Same reason tokens keep their expiry in a field. |
| D6 | Three scopes: **`read`** (every `GET` except token links and QR codes), **`tokens`** (list, create, revoke creation tokens, and read their links and QR codes), **`admin`** (everything, including purge and delete). A key holds one or more; `admin` implies the other two. | Covers the real uses — monitoring, onboarding automation, full control — without a per-resource matrix. A token link is a credential, so plain `read` does not get it. |
| D7 | API keys are **created and revoked only from the admin UI (signed-in session) or the `tpp apikey` command**. The API has no key-management endpoints. | A leaked key cannot mint itself a successor or a stronger sibling. |
| D8 | Expiry is **required**: 30, 90 (default) or 365 days, or never. At most **50** keys, enforced atomically in the create script. | Forgotten keys stop working on their own; the bound keeps the list and the per-key limiter maps small. |
| D9 | Revoking **deletes** the key at once. Its history stays in the audit log. Expired keys stay listed as expired until deleted, like expired tokens. | Nothing to un-revoke; no cache means revocation takes effect on the next request. |
| D10 | `last_used_at` and `last_used_ip` are written **at most once a minute** per key, from the hash already read to authenticate. | Avoids a Redis write on every request. |
| D11 | Rate limits, in memory, reusing `ratelimit.go`'s bucket: per key, **reads 10/s burst 60** and **writes 2/s burst 10** by default; per client IP plus globally for **failed authentication** (burst 10 refilled one per 6 s per IP; burst 60 refilled one per second globally). Overridable with `TPP_API_READ_RATE`, `TPP_API_READ_BURST`, `TPP_API_WRITE_RATE`, `TPP_API_WRITE_BURST`; the failed-auth limits are fixed, like the login's. | Automation needs vary more than login attempts do. In memory matches the admin sessions: one relay instance per Redis. |
| D12 | A refused request gets **429** with `Retry-After`; every authenticated response carries `RateLimit-Limit`, `RateLimit-Remaining` and `RateLimit-Reset`. A failed-auth lockout is logged at warn and audited as `apikey.limited`. | Lets clients back off correctly; mirrors `login.limited`. |
| D13 | **No on/off switch.** With no keys, every request is a 401 and the API does nothing. Clearing all keys turns it off. | Answer 7. |
| D14 | Errors are **`application/problem+json`** (RFC 9457) with a stable machine `code`: `unauthorized`, `forbidden`, `not_found`, `invalid_request`, `conflict`, `rate_limited`, `unavailable`, `internal`. `detail` is a human sentence that may change. | Clients branch on `code`, never on wording. |
| D15 | Groups and tokens are named by their **handle** (`handle.go`), exactly as in the UI. No endpoint returns a group id or lists a token value. | SPEC §4.4: a group id is a token, a credential. |
| D16 | Lists take the UI's `q`, `sort`, `dir`, `page`, `size` and return `{items, total, page, size, sort, dir}`. | Answer 14: lists are small; one parser for both surfaces. |
| D17 | Changes to a group, a tag and the settings are **`PATCH`** with only the fields to change; destructive actions are `POST …/purge` and `DELETE`. The API has no confirmation step, which the reference says plainly. | One request per change instead of the UI's one `PUT` per field; answer 8. |
| D18 | **No CORS.** The API is for scripts and servers; browsers get no preflight answer. | Answer 15: a browser would have to hold a key. |
| D19 | The description is **public**: `GET /api/v1/openapi.yaml` and `/api/v1/openapi.json` need no key. Its `info.version` is the API version (`1.x`), not the relay build; examples use obvious placeholders (`tpp_ak_xxxxxxxx_…`); the route counts against the failed-auth per-IP bucket. | Answer 16: it is the same file as the docs site and holds nothing about the relay; the three measures keep it from adding fingerprinting or spam surface. |
| D20 | **Versioning:** within `/api/v1`, changes only add (new endpoints, new optional fields, new `code` values). Anything that would break a caller waits for `/api/v2`. | Answer 17. |
| D21 | **Spec-first with a generator:** `api/openapi.yaml` is hand-written; [`oapi-codegen`](https://github.com/oapi-codegen/oapi-codegen) v2 (Apache-2.0) generates models and a **strict server** for the standard library `net/http` mux into `server/internal/admin/apiv1/apiv1.gen.go`. Per-operation scopes are declared as security requirements in the spec and read by the auth middleware from the generated context values. | Answer 9: handlers implement a generated interface, so a route in the spec without a handler — or with the wrong request or response types — does not compile. |
| D22 | The generator is a **`tool` directive** in `server/go.mod`, so it is pinned and never shipped. The runtime dependency `github.com/oapi-codegen/runtime` (Apache-2.0) and its own dependencies are called out in the PR (conventions §9) and picked up by `scripts/notices`. | Reproducible generation without a global install. |
| D23 | **`api/` is MPL-2.0.** `make api` copies `api/openapi.yaml` verbatim to `server/internal/admin/apiv1/openapi.yaml` (embedded with `go:embed`; Go cannot embed from outside its module), renders `openapi.json` next to it, and runs the generator. The copies stay MPL-2.0 as files; the generated Go code is AGPL like the rest of `server/`. `LICENSING.md` and `CLAUDE.md` say so. | Answer 11: integrators may generate clients from the description under any licence, as with `proto/`. |
| D24 | `make api-check` fails when regenerating changes a committed file, like `proto-check`; `go.yml` runs it and gains the `api/**` path filter. | Generated code never drifts from the description. |
| D25 | Writes made with a key are audited like the UI's, with a new **actor**: `AuditEntry.Actor` is empty for an admin session (so old entries read correctly), `apikey:<id>` for a key (the key's name is shown beside it), `cli` for the command line. Reads are not audited. | The audit log has to answer "who did this" once there is more than one kind of admin. |
| D26 | Shared logic moves out of the UI handlers into methods that return results and package errors (`errNotFound`, `errConflict`, an `invalidError` with its message). The UI handlers turn those into their sentences, the API handlers into problem codes. | The UI and the API cannot drift apart in what they allow. |
| D27 | Docs use **`starlight-openapi`** (MIT): it renders the description into native Starlight pages under `/two-place-paste/api/`, with our theme, search and link validator. An **API docs** button in the site header links to it. | Answer 10: no Swagger UI, and it looks like the rest of the docs. |

## 4. Work items

### 4.1 The description — `api/`

- `api/openapi.yaml` (OpenAPI 3.1): servers relative (`/api/v1`), one security
  scheme `apiKey` (HTTP bearer, format `tpp_ak_…`), scopes declared per
  operation, shared schemas (`Problem`, `Page`, `Limit`, `Group`, `GroupDetail`,
  `Device`, `Tag`, `Token`, `NewToken`, `CreatedToken`, `Settings`, `AuditEntry`,
  `Dashboard`, `Health`), the rate-limit headers and 401/403/404/429 responses
  as reusable components, and a tag list (Groups, Tags,
  Tokens, Settings, Audit, Relay) that becomes the reference's sidebar.
- `api/README.md`: what the folder is, that it is MPL-2.0, and `make api`.

Endpoints (scope in brackets):

| Method and path | Scope |
|---|---|
| `GET /openapi.yaml`, `GET /openapi.json` | public |
| `GET /dashboard` — storage, counts, last sweep | read |
| `GET /health` — build, uptime, Redis policy, AOF, memory | read |
| `POST /sweep` | admin |
| `GET /groups`, `GET /groups/{handle}` (with devices) | read |
| `PATCH /groups/{handle}` — name, tag, limit, store, store limit | admin |
| `POST /groups/{handle}/purge`, `POST /groups/{handle}/store/purge`, `DELETE /groups/{handle}` | admin |
| `GET /tags`, `GET /tags/{name}` | read |
| `POST /tags`, `PATCH /tags/{name}` (limits), `DELETE /tags/{name}` | admin |
| `GET /tokens`, `GET /tokens/{handle}` | tokens |
| `POST /tokens` — returns the creation URL once | tokens |
| `GET /tokens/{handle}/link`, `GET /tokens/{handle}/qr.png` | tokens |
| `DELETE /tokens/{handle}` — unused tokens only, else 409 | tokens |
| `GET /settings` | read |
| `PATCH /settings` | admin |
| `GET /audit` | read |

### 4.2 Store — `server/internal/store`

- `APIKey{ID, Name, Hash, Scopes, CreatedAt, ExpiresAt, LastUsedAt, LastUsedIP, CreatedBy}`,
  keys `apikey:<id>` and `apikeys` in `keys.go`.
- `CreateAPIKey` (Lua: check the 50 bound with `SCARD`, refuse an id collision,
  write hash and set), `GetAPIKey`, `ListAPIKeys`, `DeleteAPIKey`,
  `TouchAPIKey(id, at, ip)`. Memory and Redis implementations, shared tests.
- `AuditEntry.Actor` (omitempty).

### 4.3 Auth, limits and handlers — `server/internal/admin`

- `apikey.go`: key minting and parsing, scope checks, the middleware that turns
  a request into an authenticated key (or a problem response), `last_used`
  throttling.
- `ratelimit.go`: a keyed limiter for per-key read/write buckets alongside the
  login limiter; the failed-auth limiter is a second instance of the existing
  one.
- `apiv1/`: generated code, the embedded description and `openapi.json`.
- `api_v1_*.go`: the strict-server implementation, one file per resource,
  calling the shared methods from D26.
- `register.go`: mounts the generated handler under `/api/v1/` with the
  middleware; the `/{token}` wildcard is single-segment and never shadows it.
- Logs name a key by id only; `route` is the pattern, never the path.

### 4.4 Admin UI — `server/ui`, plus its endpoints

- Private endpoints: `GET /admin/api/api-keys`, `POST /admin/api/api-keys`,
  `DELETE /admin/api/api-keys/{id}` (session + CSRF like the rest).
- A new **API keys** screen (`/admin/api-keys`, in the nav): a smart table
  (name, id, scopes, created, expires, last used, status); a **New API key**
  dialog (name, scope checkboxes, expiry 30/90/365/never, default 90) whose result
  shows the key once with a copy button and a warning that it will not be shown
  again; revoke with a confirmation; an **API docs** link to the docs site.
- Audit screen: a **By** column showing the actor; labels for `apikey.create`,
  `apikey.revoke`, `apikey.limited`.

### 4.5 CLI — `server/cmd/tpp`

- `tpp apikey create --name NAME --scope read|tokens|admin[,…] [--expires 30d|90d|365d|never]`
  prints the key once; `tpp apikey list`; `tpp apikey revoke ID`.
- It loads the relay's configuration, connects to the same Redis, does the one
  thing and exits. It works with the relay running or stopped, and the relay
  picks the change up on the next request. Audited with actor `cli`.

### 4.6 Config — `server/internal/config`

- `TPP_API_READ_RATE`, `TPP_API_READ_BURST`, `TPP_API_WRITE_RATE`,
  `TPP_API_WRITE_BURST` (requests per second and bucket size, positive).
  Documented in `self-hosting/configuration.md` and `deploy/.env.example`.

### 4.7 Docs site — `docs/site`

- `starlight-openapi` pinned with `~`, reading `../../api/openapi.yaml`, base
  `api`, its sidebar group "Admin API reference" under "Run your own server".
  First check its supported Starlight range covers 0.42.
- **API docs** header button: a Starlight component override next to the
  social icons, linking to `/two-place-paste/api/`.
- New page `self-hosting/api.mdx`: what the API is for, creating a key (UI and
  CLI), scopes, expiry and revocation, rate limits and headers, errors and
  codes, the versioning promise, curl examples, and that destructive calls
  have no confirmation.
- Update `self-hosting/admin.mdx` (API keys screen, audit "By" column, new
  screenshots), `self-hosting/reverse-proxy.mdx` (endpoint table: `/api/v1/`;
  forward the `Authorization` header), `self-hosting/configuration.md`.
- `docs.yml` path filter gains `api/**`.

### 4.8 Specs, licensing and contributor docs

- SPEC §4.4: an "Admin API" part — scopes, key handling, rate limits, the no
  key-management-through-the-API rule, the public description.
- `LICENSING.md`: `api/` in the MPL list; one sentence on why it is MPL (others
  build and generate clients from it); the exception to "generated code follows
  its folder" for the verbatim copies under `server/`.
- `CLAUDE.md`: `api/` in the layout and licensing tables, `make api` /
  `make api-check` in the build list, `api/**` among the shared contracts.
- ROADMAP: the new item and its status.

## 5. Order of work (one PR, these commits)

1. This plan.
2. `api/openapi.yaml`, `make api` / `api-check`, generator tool directive,
   generated code, licensing files (`LICENSING.md`, `CLAUDE.md`).
3. Store: API keys and the audit actor.
4. Shared-logic refactor of the admin handlers (D26), no behaviour change.
5. Auth middleware, rate limits, `/api/v1` handlers.
6. `tpp apikey`.
7. Admin UI: API keys screen, audit actor.
8. Config variables.
9. CI: `go.yml` (`api-check`, `api/**`) and `docs.yml` path filters.
10. Docs site, SPEC, ROADMAP, screenshots.

## 6. Testing

- Store: create, the 50 bound, collision, list, delete, touch — memory and
  Redis, table-driven.
- Auth: missing header, wrong scheme, malformed key, unknown id, wrong secret,
  expired, missing scope, `admin` implying the others, success; each with the
  problem `code` and that no key material reaches the log (a captured
  `slog` handler).
- Limits: per-key read and write buckets, failed-auth per IP and global,
  headers and `Retry-After`, all on a fake clock.
- Handlers: each endpoint's success and error paths through the real mux,
  including that `/api/v1/` never accepts the admin cookie, that list
  responses contain no token value or group id, and that the `/{token}`
  routes still answer as before.
- Generated code: `make api-check`.
- UI: vitest for the API keys screen (create shows the key once, revoke).
- CLI: create/list/revoke against the memory store.
- Docs: `npm run build` in `docs/site` (also validates links into the reference).

## 7. Security review points

- No key, secret or hash in a log, an error, the audit log or an API response
  other than the one creation response.
- Revocation and expiry take effect on the next request; nothing caches a key.
- No API path can create, change or reveal an API key.
- `/api/v1/` ignores cookies entirely and answers no preflight.
- Body size bounded by `http.MaxBytesReader` before decoding, as in the UI API.
- A token link and QR code need the `tokens` scope.

## 8. Risks

- `starlight-openapi` may lag Starlight releases; if 0.42 is not supported yet,
  pin the newest compatible pair or render the reference with the plugin's
  previous major until it catches up.
- oapi-codegen's strict server writes its own error responses for bad
  parameters; they are routed through our problem+json writer with its
  `RequestErrorHandlerFunc` / `ResponseErrorHandlerFunc` hooks.

## 9. Answered questions

1. Where does the API live? `/api/v1/`, separate from the UI's API.
2. Scopes? `read`, `tokens`, `admin`.
3. Can a key manage keys? No.
4. Expiry? Required: 30, 90 (default), 365 days or never; at most 50 keys.
5. Revoked keys? Deleted; history in the audit log.
6. Rate limits? Fixed defaults with `TPP_API_*_RATE/BURST` overrides.
7. Kill switch? None; no keys means no API.
8. Purge and delete through the API? Yes, with `admin`.
9. Keeping the description honest? A proper generator: oapi-codegen.
10. Docs renderer? `starlight-openapi`.
11. Licence of the description? MPL-2.0, in `api/` at the root.
12. A command-line key command? Yes, `tpp apikey`.
13. Token links and QR codes through the API? Yes, with `tokens`.
14. Paging? The UI's `q/sort/dir/page/size`.
15. CORS? None.
16. Is the description public? Yes, with the measures in D19.
17. Versioning? Additive within v1; breaking changes wait for v2.
18. Separate PRs? No — one PR, shared contracts in their own commits.
