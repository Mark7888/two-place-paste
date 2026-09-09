module github.com/Mark7888/two-place-paste/server

go 1.26

// Direct dependencies. Each one is a thing the operator must trust and the
// maintainer must update (docs/conventions.md §9):
//
//   - coder/websocket: the transport of SPEC §5.1. Context-aware, no
//     third-party dependencies of its own, and it does not bring a router or a
//     logger with it.
//   - redis/go-redis: the only datastore (SPEC §4.2). Needed for its EVALSHA
//     script support, which is what makes the rekey and the token CAS atomic.
//   - google.golang.org/protobuf: the wire contract from ROADMAP P1.
//   - rsc.io/qr: QR encoding for the admin UI's token codes (SPEC §4.4).
//     Standard library plus one file's worth of encoder; it pulls in nothing
//     and it writes a PNG, so the admin screen needs no image dependency
//     either.
//
// The workspace supplies pkg/tppclient (the generated protobuf bindings); it
// is not a module dependency.
require (
	github.com/coder/websocket v1.8.15
	github.com/redis/go-redis/v9 v9.22.0
	google.golang.org/protobuf v1.36.12
	rsc.io/qr v0.2.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
)
