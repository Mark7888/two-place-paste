#!/usr/bin/env bash
#
# Regenerate the protobuf bindings for both target languages (ROADMAP P1).
#
# Invoked by `make proto`. Generated output is committed, and `make proto-check`
# re-runs this script and fails if anything changed — so this script must be
# idempotent: the same schema in, byte-identical output out.
#
# Tool versions are pinned in two lockfiles, never fetched at "latest":
#   tools/go.mod      buf, protoc-gen-go
#   package-lock.json ts-proto
#
# No protoc binary is required. buf compiles .proto files itself.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

# The tools module is deliberately not in /go.work (see tools/go.mod).
export GOWORK=off

BIN="$PWD/.bin"
mkdir -p "$BIN"

echo "==> building pinned codegen plugins"
go build -C tools -o "$BIN/buf" github.com/bufbuild/buf/cmd/buf
go build -C tools -o "$BIN/protoc-gen-go" google.golang.org/protobuf/cmd/protoc-gen-go

echo "==> installing pinned TypeScript codegen plugin"
npm ci --no-audit --no-fund --silent

export PATH="$BIN:$PWD/node_modules/.bin:$PATH"

echo "==> formatting schema"
buf format --write

echo "==> linting schema"
buf lint

echo "==> generating Go and TypeScript bindings"
buf generate

echo "==> typechecking generated TypeScript"
npm run --silent typecheck

echo "proto: generation complete"
