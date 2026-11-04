//go:build darwin && cgo

package clipboard

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit

#import <AppKit/AppKit.h>
#include <stdlib.h>
#include <string.h>

static long tppPasteboardChangeCount(void) {
	@autoreleasepool {
		return (long)[[NSPasteboard generalPasteboard] changeCount];
	}
}

// tppWriteConcealedText puts text on the pasteboard together with the
// nspasteboard.org concealed type, which clipboard managers take as "do not
// keep this" — what a password manager writes. It returns 0 on failure.
static int tppWriteConcealedText(const char *text) {
	@autoreleasepool {
		NSString *s = [NSString stringWithUTF8String:text];
		if (s == nil) {
			return 0;
		}
		NSString *concealed = @"org.nspasteboard.ConcealedType";
		NSPasteboard *pb = [NSPasteboard generalPasteboard];
		[pb declareTypes:@[ NSPasteboardTypeString, concealed ] owner:nil];
		BOOL ok = [pb setString:s forType:NSPasteboardTypeString];
		[pb setString:@"" forType:concealed];
		return ok ? 1 : 0;
	}
}

// tppPasteboardTypes returns the pasteboard's types joined by newlines, in
// memory the caller frees, and the change count they were read at. Type
// identifiers are UTIs and legacy names; none contains a newline.
static char *tppPasteboardTypes(long *count) {
	@autoreleasepool {
		NSPasteboard *pb = [NSPasteboard generalPasteboard];
		*count = (long)[pb changeCount];
		NSArray<NSPasteboardType> *types = [pb types];
		if (types == nil) {
			return strdup("");
		}
		const char *joined = [[types componentsJoinedByString:@"\n"] UTF8String];
		return strdup(joined != NULL ? joined : "");
	}
}
*/
import "C"

import (
	"strings"
	"unsafe"
)

// pasteboardChangeCount is NSPasteboard's changeCount: a counter the
// pasteboard server bumps on every write, by any application.
//
// Reading it is a single message to the pasteboard server, where the content
// reads in clipboard_darwin.go are a process each, and it is what lets an
// idle watcher cost nothing: the tools run only when the counter has moved.
// The module already builds with cgo on macOS — the tray needs it — so this
// adds no toolchain requirement.
func pasteboardChangeCount() (uint64, bool) {
	return uint64(C.tppPasteboardChangeCount()), true
}

// pasteboardWriteConcealed writes text marked private (markers.go), and
// reports whether it could.
func pasteboardWriteConcealed(text string) bool {
	c := C.CString(text)
	defer C.free(unsafe.Pointer(c))
	return C.tppWriteConcealedText(c) == 1
}

// pasteboardTypes returns the type identifiers on the pasteboard and the
// change count they belong to. The types are what tell a password manager's
// copy from any other (markers.go); the content tools cannot see them.
func pasteboardTypes() ([]string, uint64, bool) {
	var count C.long
	p := C.tppPasteboardTypes(&count)
	if p == nil {
		return nil, 0, false
	}
	defer C.free(unsafe.Pointer(p))
	joined := C.GoString(p)
	if joined == "" {
		return nil, uint64(count), true
	}
	return strings.Split(joined, "\n"), uint64(count), true
}
