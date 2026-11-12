#!/usr/bin/env bash
#
# Regenerate the relay's admin API code from openapi.yaml
# (docs/internal/plans/admin-rest-api.md D21-D24).
#
# Invoked by `make api`. Generated output is committed, and `make api-check`
# re-runs this script and fails if anything changed — so this script must be
# idempotent: the same description in, byte-identical output out.
#
# The generator is pinned in tools/go.mod, never fetched at "latest".
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

# The tools module is deliberately not in /go.work (see tools/go.mod).
export GOWORK=off

BIN="$PWD/.bin"
OUT=../server/internal/admin/apiv1
mkdir -p "$BIN" "$OUT"

echo "==> building pinned generator"
go build -C tools -o "$BIN/oapi-codegen" github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen
go build -C tools -o "$BIN/specjson" ./specjson

echo "==> generating Go server code"
# Run from the output folder: the generator reads the server's go.mod there.
(cd "$OUT" && "$BIN/oapi-codegen" -config "$OLDPWD/oapi-codegen.yaml" "$OLDPWD/openapi.yaml")

# Go can only embed files inside its own module, so the relay gets a verbatim
# copy of the description. The copy stays MPL-2.0 (LICENSING.md).
echo "==> copying the description into the relay"
cp openapi.yaml "$OUT/openapi.yaml"
"$BIN/specjson" openapi.yaml "$OUT/openapi.json"

echo "api: generation complete"
