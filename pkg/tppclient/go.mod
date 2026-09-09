module github.com/Mark7888/two-place-paste/pkg/tppclient

go 1.26.0

// Direct dependencies. Each one is a thing every client shipped from this
// module must trust and the maintainer must update (docs/conventions.md §9):
//
//   - coder/websocket: the transport of SPEC §5.1, the same library the server
//     speaks. Context-aware, and it brings no dependencies of its own.
//   - x/crypto: XChaCha20-Poly1305, the one non-stdlib primitive the crypto
//     profile requires (/spec/crypto.md §2.2). X25519 and HKDF are stdlib.
//   - x/sys: CryptProtectData/CryptUnprotectData for the Windows keystore
//     backend. Used on no other platform.
//   - google.golang.org/protobuf: the wire contract from ROADMAP P1.
require (
	github.com/coder/websocket v1.8.15
	golang.org/x/crypto v0.56.0
	google.golang.org/protobuf v1.36.12
)

require golang.org/x/sys v0.47.0
