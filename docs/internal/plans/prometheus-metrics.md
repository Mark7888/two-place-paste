# Plan — Prometheus metrics listener for the relay

**Status:** planned, not started.
**Touches:** `/server/**` (`internal/config`, new `internal/metrics`, `internal/ws`,
`internal/entries`, `internal/items`, `internal/httpapi`, `cmd/tpp`), `/deploy/**`,
`/docs/site/**`, SPEC §4.

---

## 0. Decisions

- Output is produced with `github.com/prometheus/client_golang`.
- First version exposes runtime/process metrics, scrape-time state gauges, and
  event counters and histograms. **No per-group series** for now: every metric is
  relay-wide.
- The endpoint is protected by an optional bearer token.
- The listen address is configured as separate host and port, defaulting to
  `127.0.0.1:9090`, and the listener is off by default.
- Everything ships in one PR, with a compose port mapping and a new docs page.
- `/metrics` is served only on its own listener, never on the relay's mux, so a
  reverse proxy in front of the relay cannot expose it by accident.

## 1. Configuration (`server/internal/config`)

| Variable | Default | Meaning |
|---|---|---|
| `TPP_METRICS_ENABLED` | `false` | Turns on the second listener. |
| `TPP_METRICS_HOST` | `127.0.0.1` | Bind host. |
| `TPP_METRICS_PORT` | `9090` | Bind port. |
| `TPP_METRICS_TOKEN` / `_FILE` | empty | If set, every request must send `Authorization: Bearer <token>`. Read with `r.secret` so Docker secrets work. |

- Add a `MetricsConfig{Enabled bool; Host string; Port int; Token string}` field
  to `Config`, with an `Addr()` helper returning
  `net.JoinHostPort(Host, strconv.Itoa(Port))`.
- `Validate()` checks the settings only when metrics are enabled:
  - the port is between 1 and 65535;
  - the host is not empty;
  - `Addr()` differs from `TPP_HTTP_ADDR`. This is best-effort: it compares
    ports after resolving `:8080`-style addresses, and catches the obvious case
    of both listeners on the same port.
- When metrics are disabled the settings are ignored, so a stale port value
  cannot stop the relay from starting.
- The token is never logged.
- `config_test.go` gains table cases for the defaults, enabled and valid, a bad
  port, a port clash, token vs `_FILE` conflicts, and "disabled ignores a bad
  port".

## 2. New package `server/internal/metrics`

The package doc cites the SPEC and states the privacy rule: **labels carry only
bounded, non-secret values** — message type, error code, route pattern, result,
backend. Never group or device IDs, sizes as labels, or anything derived from
content.

**`metrics.go`**
- A private `*prometheus.Registry`, not the global default, so tests stay
  isolated and nothing leaks into it from libraries.
- Registers `collectors.NewGoCollector()`, `collectors.NewProcessCollector()`,
  and `tpp_build_info{version}` (from `buildVersion()`) with value 1.

**`handler.go`**
- `promhttp.HandlerFor(reg, …)` wrapped in token auth
  (`subtle.ConstantTimeCompare`; a 401 carries `WWW-Authenticate: Bearer`),
  `GET` only.
- The mux serves only `GET /metrics`; anything else on that port is a 404.

**`state.go`** — a custom `prometheus.Collector` read at scrape time. Its
interfaces are defined here, at the consumer, and are satisfied by existing
types:

| Metric | Source |
|---|---|
| `tpp_ws_connections`, `tpp_ws_devices_online`, `tpp_ws_groups_online` | new `ws.Server.Stats()` (§3) |
| `tpp_blob_used_bytes`, `tpp_blob_entries` | `quota.Ledger.Usage` |
| `tpp_blob_limit_bytes{scope="total"\|"group"}` | config (0 = unlimited) |
| `tpp_blob_evicted_entries_24h`, `tpp_blob_evicted_bytes_24h` | `Ledger.Evictions` (24 h window gauges, as on the dashboard) |
| `tpp_store_used_bytes`, `tpp_store_items` | `store.TotalStoreUsage` |
| `tpp_uploads_in_progress`, `tpp_uploads_reserved_bytes` | `entrySvc.UploadsInProgress` |
| `tpp_sweep_last_run_timestamp_seconds`, `tpp_sweep_last_duration_seconds`, `tpp_sweep_last_success` | `sweeper.Status()` |
| `tpp_redis_up` | `PING` |
| `tpp_scrape_errors_total{source}` | its own counter |

- The collector uses a bounded context (~5 s) derived from the scrape request.
- A failing source skips its metric, increments
  `tpp_scrape_errors_total{source}` and logs at Warn; it does not fail the
  whole scrape.

**`events.go`** — a `Recorder` struct holding the event metrics and
implementing the small observer interfaces the domain packages declare (§3).

| Metric | Labels |
|---|---|
| `tpp_ws_connections_opened_total`, `tpp_ws_connections_closed_total` | — |
| `tpp_ws_messages_total` | `type`, `result` (`ok` or the `ErrorCode` name) |
| `tpp_ws_message_duration_seconds` (histogram) | `type` |
| `tpp_entries_put_total` | `path` = `inline`, `blob` or `chunked` |
| `tpp_entries_put_bytes_total` | ciphertext bytes accepted |
| `tpp_entries_fetched_total`, `tpp_entries_fetched_bytes_total` | — |
| `tpp_entries_evicted_total`, `tpp_entries_evicted_bytes_total` | `reason` = `quota` |
| `tpp_uploads_total` | `result` = `completed`, `aborted`, `reaped` or `rejected_quota` |
| `tpp_store_items_saved_total`, `tpp_store_items_deleted_total`, `tpp_store_saves_rejected_total` | — |
| `tpp_pairings_total` | `result` |
| `tpp_groups_created_total`, `tpp_rekeys_total` | — |
| `tpp_http_requests_total`, `tpp_http_request_duration_seconds` | `route` (mux pattern, e.g. `GET /admin/api/groups`), `code` class |

- `type` is the `MessageType` enum name, a bounded set. A type the server does
  not know is recorded as `unknown`, because clients control that value.
- `tpp_http_*` uses `r.Pattern`, never the raw path; requests matching no route
  are recorded as `unmatched`.
- WebSocket upgrades are counted as connections, not timed as HTTP requests.
- Histogram buckets: the defaults for HTTP; 1 ms to 10 s for WebSocket
  messages, because chunk uploads take longer.
- `tpp_blob_evicted_*_24h` are gauges because the ledger keeps only a 24 h
  window; `tpp_entries_evicted_total` is the counter to use with `rate()`.

## 3. Instrumenting the domain packages

Each package declares a small interface where it is used, keeps a no-op
default, and does not import `prometheus`. With metrics disabled `main` passes
nil, so the cost is one interface call against a no-op.

- **`ws`**: `ws.Options.Metrics` (`ConnOpened/Closed`,
  `Message(type, code, dur)`, `Pairing(result)`, `GroupCreated`, `Rekey`).
  Hook points:
  - one wrap around `dispatch` in `handlers.go`, which covers every message
    including `storeHandler`;
  - `Hub.register` / `unregister`;
  - the result points in `handlePairing*`, `handleCreateGroup` and
    `handleRekey`.
- **`ws.Server.Stats()`** (new): relay-wide connection, device and group
  counts, read under the `Hub` lock from the maps it already keeps.
- **`entries`**: `entries.Options.Metrics` (`Put(path, bytes)`,
  `Fetched(bytes)`, `Evicted(n, bytes)`, `Upload(result)`). Hook points:
  `PutWithHeader`, `Fetch` / `Latest` / chunk fetch, the `OnEvict` path, and
  the upload end states in `uploads.go`.
- **`items`**: `items.Options.Metrics` (`Saved`, `Deleted`, `SaveRejected`).
  Hook points: `Put`, `Delete`, and the quota-refusal branch in `save.go`.
- **`httpapi`**: `Options.Metrics` with a middleware next to `recoverer` that
  records `r.Pattern`, the status and the duration. It wraps the
  `ResponseWriter` but keeps `http.Hijacker` working so WebSocket upgrades
  still go through.

  `httpapi/router.go` carries a "shared touchpoint, open a contract-change
  issue" header from the ROADMAP phase days. The edit is additive (one optional
  field), so it is done in this PR and called out in the description.

## 4. Wiring (`cmd/tpp/main.go`)

- When `cfg.Metrics.Enabled`, build `metrics.New(...)`, pass its `Recorder`
  into the `ws` / `entries` / `items` / `httpapi` options, and register the
  state collector once those components exist.
- Start a second `http.Server{Addr: cfg.Metrics.Addr(), ReadHeaderTimeout,
  ReadTimeout/WriteTimeout ~30s, ErrorLog}`. Unlike the relay listener, which
  has no deadlines because of WebSockets, this one can have them.
- Lifecycle:
  - the metrics server runs in its own goroutine and reports into the same
    `serveErr` select, so a bind failure (port in use) stops the relay at
    startup with a clear error;
  - shutdown calls `Shutdown` on the metrics server alongside the main server,
    within the same 15 s grace.
- Log the address at startup (`"metrics listening"`, `addr`, `auth`). Warn if
  the host is not loopback and no token is set.

## 5. Dependency

- Add `github.com/prometheus/client_golang` (Apache-2.0) to `server/go.mod`.
- Extend the dependency comment block in `go.mod`: what it is for, and that it
  brings `client_model`, `common` and `procfs`, all Apache-2.0.
- The PR description calls out the new module (conventions §9).
- `make tidy` for `go.work.sum`. The Docker build's notices step picks it up.
- Server-only: AGPL-compatible, and it never reaches the MPL clients.

## 6. Deployment

- **`deploy/docker-compose.yml`**: `TPP_METRICS_HOST: "0.0.0.0"` in the
  server's `environment` (safe: it applies inside the container network only,
  and does nothing unless enabled), and the port mapping
  `"${TPP_METRICS_BIND_ADDR:-127.0.0.1}:${TPP_METRICS_HOST_PORT:-9090}:9090"`
  with a comment in the style of the `TPP_HOST_BIND_ADDR` warning.
- **`deploy/Dockerfile`**: `EXPOSE 9090`.
- **`deploy/.env.example`**: a commented `TPP_METRICS_*` block plus the two
  compose-side bind variables.

## 7. Docs (same PR)

- **New page `docs/site/src/content/docs/self-hosting/monitoring.md`**, in the
  sidebar in `astro.config.mjs`: enabling the listener, choosing a bind
  address, the token, the compose mapping, a sample `prometheus.yml`
  `scrape_config` (with `authorization: credentials_file`), the full metric
  table, and the privacy statement that no group or device IDs or
  content-derived data appear.
- **`self-hosting/configuration.md`**: the four variables, linking to the new
  page.
- **`docs/internal/SPECS.md`**: a short §4 subsection on the metrics listener
  and its label rule, so code can cite it.
- **`docs/internal/ROADMAP.md`**: a status line if there is a matching entry.
- `npm run build` in `docs/site` passes.

## 8. Tests

- **`internal/metrics`**:
  - handler: with and without a token, wrong token, non-GET is 405, other
    paths are 404;
  - state collector with fake sources: values appear, and a failing source
    only increments `tpp_scrape_errors_total`;
  - `Recorder` counters via `prometheus/testutil.ToFloat64` (part of
    `client_golang`; the no-testify rule still holds);
  - a test that collects every registered metric and asserts no label name is
    in a deny-list (`group`, `device`, `id`, …).
- **`ws` / `entries` / `items` / `httpapi`**: a fake observer per package, with
  table cases asserting the right calls on success and failure paths, plus a
  test that the HTTP middleware still lets a WebSocket upgrade through.
- **`cmd/tpp`**: config tests only, optionally a smoke test that starts the
  metrics server on `127.0.0.1:0`.
- Locally: `make build test lint`, `make fmt-check`, and the docs build.

## 9. Deferred

- Per-group series (a `group_id` label on usage gauges). They cost 2–3 Redis
  calls per group per scrape; if added later, gate them behind their own
  opt-in variable and cache the snapshot for a short TTL.
