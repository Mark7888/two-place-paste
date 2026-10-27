package tray

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Cocoa

void tppWatchReopen(void);
*/
import "C"

import "sync"

// Opening the app again on macOS.
//
// Double-clicking a running app, or choosing it in Spotlight or Launchpad,
// does not start a second copy: macOS sends the running one a "reopen" Apple
// Event instead. An agent app has no window to bring forward, so without a
// handler nothing at all happened. reopen_darwin.m takes the event and this
// file hands it to the tray, which opens the app in the browser as its "Open
// TwoPlacePaste" item does.

var (
	reopenMu sync.Mutex
	onReopen func()
)

// watchReopen calls fn each time the app is opened again while it runs.
func watchReopen(fn func()) {
	reopenMu.Lock()
	onReopen = fn
	reopenMu.Unlock()
	C.tppWatchReopen()
}

//export tppReopen
func tppReopen() {
	reopenMu.Lock()
	fn := onReopen
	reopenMu.Unlock()
	if fn != nil {
		// Off the main thread, which the event arrived on and which the menu
		// bar needs.
		go fn()
	}
}
