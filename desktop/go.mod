module github.com/Mark7888/two-place-paste/desktop

go 1.26.0

// The client core is developed in this repository alongside the desktop
// service; the workspace resolves it locally and this keeps a module-mode
// build (GOWORK=off, which is what `go mod tidy` in CI does) resolving too.
replace github.com/Mark7888/two-place-paste/pkg/tppclient => ../pkg/tppclient

// Direct dependencies. The desktop service is a shell around pkg/tppclient and
// holds no crypto and no protocol logic of its own (ROADMAP P5), so this list
// is deliberately short:
//
//   - pkg/tppclient: every flow of SPEC §3 and §6.
//   - coder/websocket: the localhost UI's event stream, the same library the
//     relay and the client speak.
//   - fyne.io/systray: the tray icon of SPEC §7.2. Built only on Windows and
//     macOS; the Linux build uses the headless tray in internal/tray.
//   - x/sys: the Windows registry Run key and the Windows keystore backend.
//     Pinned to the version pkg/tppclient uses so the workspace builds both
//     against one release.
require (
	fyne.io/systray v1.12.2
	github.com/Mark7888/two-place-paste/pkg/tppclient v0.0.0-00010101000000-000000000000
	github.com/coder/websocket v1.8.15
	golang.org/x/sys v0.47.0
)

require (
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
