module github.com/Mark7888/two-place-paste/server

go 1.26.0

// Direct dependencies. Each one is a thing the operator must trust and the
// maintainer must update (docs/internal/conventions.md §9):
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
//   - minio/minio-go: the S3 blob backend (SPEC §4.3). One client that speaks
//     to MinIO and AWS S3 alike, with a far smaller tree than aws-sdk-go-v2.
//     Only the s3 backend uses it; a disk deployment never opens a
//     connection with it.
//
// The workspace supplies pkg/tppclient (the generated protobuf bindings); it
// is not a module dependency.
require (
	github.com/coder/websocket v1.8.15
	github.com/minio/minio-go/v7 v7.4.1
	github.com/redis/go-redis/v9 v9.22.0
	google.golang.org/protobuf v1.36.12
	rsc.io/qr v0.2.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/klauspost/crc32 v1.3.0 // indirect
	github.com/minio/crc64nvme v1.1.1 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	gopkg.in/ini.v1 v1.67.3 // indirect
)
