// The interop test's Go peer. Standalone by design: a test tool that is not
// part of the workspace (see the package comment in main.go). Build it with
// GOWORK=off.
module github.com/Mark7888/two-place-paste/mobile/scripts/gopeer

go 1.26.0

require github.com/Mark7888/two-place-paste/pkg/tppclient v0.0.0

require (
	github.com/coder/websocket v1.8.15 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/Mark7888/two-place-paste/pkg/tppclient => ../../../pkg/tppclient
