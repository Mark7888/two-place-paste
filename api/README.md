# Admin API description

`openapi.yaml` describes the relay's public admin REST API, served under
`/api/v1/` (docs/internal/plans/admin-rest-api.md). It is the source of truth:
the relay's server code is generated from it, the relay serves it at
`/api/v1/openapi.yaml` and `/api/v1/openapi.json`, and the docs site renders
its reference from it.

This directory is a **shared contract**, like `proto/`. Within `/api/v1`, a
change may only add: new endpoints, new optional request fields, new response
fields and new error codes. Anything that would break a caller waits for
`/api/v2`.

| File | What it is |
|---|---|
| `openapi.yaml` | The description (OpenAPI 3.1) |
| `oapi-codegen.yaml` | Generator settings |
| `generate.sh` | Regenerates the relay's code; run through `make api` |
| `tools/` | The pinned generator, and `specjson`, which renders the description as JSON. Outside `/go.work`, like `proto/tools`. |

After editing `openapi.yaml`, run `make api` and commit what it changes in
`server/internal/admin/apiv1/`: the generated `apiv1.gen.go` and the relay's
copies of the description. `make api-check`, run in CI, fails when they are
stale. Never edit the generated files by hand.

## Licence

Unlike `server/`, this directory is MPL-2.0 (the repository default), so that
anyone can build or generate a client for the API under any licence. The
copies `make api` writes into `server/internal/admin/apiv1/` stay MPL-2.0;
see `LICENSING.md`.
