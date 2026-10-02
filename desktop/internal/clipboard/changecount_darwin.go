//go:build darwin && cgo

package clipboard

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit

#import <AppKit/AppKit.h>

static long tppPasteboardChangeCount(void) {
	@autoreleasepool {
		return (long)[[NSPasteboard generalPasteboard] changeCount];
	}
}
*/
import "C"

// pasteboardChangeCount is NSPasteboard's changeCount: a counter the
// pasteboard server bumps on every write, by any application.
//
// It is the one piece of AppKit this package reaches directly. Reading it is
// a single message to the pasteboard server, where the content reads below
// are a process each, and it is what lets an idle watcher cost nothing: the
// tools run only when the counter has moved. The module already builds with
// cgo on macOS — the tray needs it — so this adds no toolchain requirement.
func pasteboardChangeCount() (uint64, bool) {
	return uint64(C.tppPasteboardChangeCount()), true
}
